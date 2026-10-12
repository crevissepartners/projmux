package mutations

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"path/filepath"
	"strings"
)

// ResolveProjectRef is the shared explicit lifecycle selector, including absolute roots.
func ResolveProjectRef(load func() (coremetadata.Registry, error), spelling, ref string, usage func(string) error, mapError func(error) error) (coremetadata.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return coremetadata.Project{}, usage(spelling + " requires a non-empty Project reference")
	}
	if load == nil {
		return coremetadata.Project{}, fmt.Errorf("%s: the resource registry is not configured", spelling)
	}
	registry, err := load()
	if err != nil {
		return coremetadata.Project{}, mapError(err)
	}
	if filepath.IsAbs(ref) {
		project, ok := registry.ProjectByRoot(filepath.Clean(ref))
		if !ok {
			return coremetadata.Project{}, usage(fmt.Sprintf(
				"%s: no Project claims root %q; register it with `projmux create project --root %s`", spelling, ref, ref))
		}
		return *project, nil
	}
	parsed, err := selector.ParseRef(coremetadata.KindProject, ref)
	if err != nil {
		return coremetadata.Project{}, err
	}
	query := selector.Query{Project: &parsed}
	resolution, err := selector.New(registry).ResolveProjects(query)
	if err != nil {
		return coremetadata.Project{}, err
	}
	target := selector.Target{Verb: selector.VerbFocus, Kind: coremetadata.KindProject}
	if err := selector.Enforce(target, selector.DescribeSelector(query), resolution); err != nil {
		return coremetadata.Project{}, err
	}
	project, ok := registry.Project(resolution.Matches[0].UID)
	if !ok {
		return coremetadata.Project{}, fmt.Errorf("%s: project %q disappeared during preflight", spelling, resolution.Matches[0].UID)
	}
	return *project, nil
}
