// Package profiles executes stored profile operations for CLI and SDK.
package profiles

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/selector"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// Service resolves explicit paths per operation, without global state.
type Service struct {
	Paths         func() (config.Paths, error)
	RegistryPaths func() (config.Paths, error)
}

// Info contains the exact profile list table cell values, including '-' blanks.
type Info struct {
	Name, Source, Provider, Instructions, Model, Effort, Roles, Digest, Valid, Scope string
}

// WriteResult describes the file replaced by Write.
type WriteResult struct {
	Name, Path, Digest string
	Size               int64
}

// ProjectError distinguishes Registry/selector failures from filesystem errors.
type ProjectError struct{ Err error }

func (e *ProjectError) Error() string { return e.Err.Error() }
func (e *ProjectError) Unwrap() error { return e.Err }

// ConfirmationError represents the CLI deletion confirmation gate.
type ConfirmationError struct{ Name string }

func (e *ConfirmationError) Error() string {
	return fmt.Sprintf("profile delete %s requires --yes; nothing was deleted", e.Name)
}

func (s Service) store(read bool) (profile.Store, error) {
	paths, err := s.Paths()
	if read && errors.Is(err, config.ErrHomeDirRequired) {
		return profile.NewUnavailableStore(err), nil
	}
	if err != nil {
		return profile.Store{}, err
	}
	return profile.NewDefaultStore(paths), nil
}

func (s Service) List(ctx context.Context, projectRef string) ([]Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := s.store(true)
	if err != nil {
		return nil, err
	}
	entries, err := store.List()
	if err != nil {
		return nil, err
	}
	projectUID := ""
	if projectRef != "" {
		ref, err := selector.ParseRef(metadata.KindProject, projectRef)
		if err != nil {
			return nil, &ProjectError{err}
		}
		paths, err := s.RegistryPaths()
		if err != nil {
			return nil, &ProjectError{err}
		}
		registry, err := storemetadata.NewDefaultStore(paths).LoadReadOnly()
		if err != nil {
			return nil, &ProjectError{err}
		}
		query := selector.Query{Project: &ref}
		resolved, err := selector.New(registry).ResolveProjects(query)
		if err != nil {
			return nil, &ProjectError{err}
		}
		if err := selector.Enforce(selector.Target{Verb: selector.VerbGet, Kind: metadata.KindProject}, selector.DescribeSelector(query), resolved); err != nil {
			return nil, &ProjectError{err}
		}
		projectUID = resolved.Matches[0].UID
	}
	out := make([]Info, 0, len(entries))
	for _, entry := range entries {
		if projectUID != "" && !profile.VisibleInProject(entry.Project, projectUID) {
			continue
		}
		out = append(out, Project(entry))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Project projects one stored entry into the existing CLI table vocabulary.
func Project(entry profile.Entry) Info {
	cell := func(value string) string {
		if value == "" {
			return "-"
		}
		return value
	}
	scope := entry.Project
	if scope == "" {
		scope = "global"
	}
	valid := "yes"
	if !entry.Valid {
		valid = "no (" + entry.Reason + ")"
	}
	return Info{entry.Name, entry.Source, cell(entry.Provider), cell(entry.Instructions), cell(entry.Model), cell(entry.Effort), cell(strings.Join(entry.Roles, ",")), cell(entry.Digest), valid, scope}
}

func (s Service) Read(ctx context.Context, name string) (profile.Profile, error) {
	if err := ctx.Err(); err != nil {
		return profile.Profile{}, err
	}
	store, err := s.store(true)
	if err != nil {
		return profile.Profile{}, err
	}
	return store.Load(name)
}

func (s Service) Write(ctx context.Context, name string, content []byte) (WriteResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	if err := profile.ValidateName(name); err != nil {
		return WriteResult{}, err
	}
	// Match CLI ReadLimited before path resolution, including its refusal text.
	content, err := profile.ReadLimited(bytes.NewReader(content))
	if err != nil {
		var refusal *profile.Error
		if errors.As(err, &refusal) {
			refusal.Name = name
		}
		return WriteResult{}, err
	}
	store, err := s.store(false)
	if err != nil {
		return WriteResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	entry, err := store.Write(name, content)
	if err != nil {
		return WriteResult{}, err
	}
	return WriteResult{Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Size: int64(len(content))}, nil
}

func (s Service) Delete(ctx context.Context, name string, confirm bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := profile.ValidateName(name); err != nil {
		return err
	}
	if !confirm {
		return &ConfirmationError{Name: name}
	}
	store, err := s.store(false)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.Delete(name)
}
