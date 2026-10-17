package agentguidance

import (
	"context"

	"github.com/crevissepartners/projmux/internal/config"
	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

func (s Service) project(ctx context.Context, projectRef string) (guidance.ProjectStore, string, error) {
	if err := ctx.Err(); err != nil {
		return guidance.ProjectStore{}, "", err
	}
	ref, err := selector.ParseRef(metadata.KindProject, projectRef)
	if err != nil {
		return guidance.ProjectStore{}, "", resources.Classify(err)
	}
	var paths config.Paths
	paths, err = s.RegistryPaths()
	if err != nil {
		return guidance.ProjectStore{}, "", err
	}
	registry, err := resources.Load(paths)
	if err != nil {
		return guidance.ProjectStore{}, "", err
	}
	query := selector.Query{Project: &ref}
	resolved, err := selector.New(registry).ResolveProjects(query)
	if err == nil {
		err = selector.Enforce(selector.Target{Verb: selector.VerbDescribe, Kind: metadata.KindProject}, selector.DescribeSelector(query), resolved)
	}
	if err != nil {
		return guidance.ProjectStore{}, "", resources.Classify(err)
	}
	paths, err = s.Paths()
	if err != nil {
		return guidance.ProjectStore{}, "", err
	}
	store := guidance.NewDefaultProjectStore(paths)
	uid := resolved.Matches[0].UID
	if _, err := store.Path(uid); err != nil {
		return guidance.ProjectStore{}, "", &resources.Failure{Code: "selector-invalid", Cause: err}
	}
	return store, uid, ctx.Err()
}
func (s Service) ReadProject(ctx context.Context, ref string) (guidance.ProjectFile, error) {
	store, uid, err := s.project(ctx, ref)
	if err != nil {
		return guidance.ProjectFile{}, err
	}
	return store.ReadFile(ctx, uid)
}
func (s Service) ValidateProject(ctx context.Context, ref string, b []byte) (guidance.ProjectValidation, error) {
	store, uid, err := s.project(ctx, ref)
	if err != nil {
		return guidance.ProjectValidation{}, err
	}
	return store.ValidateFile(ctx, uid, b)
}
func (s Service) WriteProject(ctx context.Context, ref string, b []byte, revision string) (guidance.ProjectFile, error) {
	store, uid, err := s.project(ctx, ref)
	if err != nil {
		return guidance.ProjectFile{}, err
	}
	return store.WriteFile(ctx, uid, b, revision)
}
func (s Service) DeleteProject(ctx context.Context, ref, revision string) (guidance.ProjectFile, error) {
	store, uid, err := s.project(ctx, ref)
	if err != nil {
		return guidance.ProjectFile{}, err
	}
	return store.DeleteFile(ctx, uid, revision)
}
