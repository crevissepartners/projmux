// Package projectcandidates owns read-only configured project discovery.
package projectcandidates

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/pins"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

type Candidate struct {
	Path       string `json:"path"`
	Pinned     bool   `json:"pinned"`
	ProjectUID string `json:"projectUID,omitempty"`
}
type Result struct {
	Candidates []Candidate `json:"candidates"`
}

// Service reads configured roots, Registry identity and pins without persisting
// discovery memory, migrating pins, or injecting home/current-path candidates.
type Service struct {
	Paths        func() (config.Paths, error)
	HomeDir      func() (string, error)
	LookupEnv    func(string) string
	TmuxProjdir  func() string
	LoadRegistry func() (metadata.Registry, error)
}

// DiscoveryInputs combines configured scan roots with resolved pin paths.
func DiscoveryInputs(homeDir, repoRoot string, pinPaths []string, lookup func(string) string, loadWorkdirs func(string) ([]string, error)) candidates.Inputs {
	return candidates.Inputs{HomeDir: homeDir, RepoRoot: repoRoot, ManagedRoots: ManagedRoots(homeDir, repoRoot, ExtraProjdirRoots(lookup), lookup, loadWorkdirs), Pins: pinPaths}
}

func (s Service) List(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	paths, err := s.Paths()
	if err != nil {
		return Result{}, err
	}
	var registry metadata.Registry
	if s.LoadRegistry != nil {
		registry, err = s.LoadRegistry()
	} else {
		registry, err = resources.Load(paths)
	}
	if err != nil {
		return Result{}, err
	}
	home, err := s.HomeDir()
	if err != nil {
		return Result{}, fmt.Errorf("resolve home directory: %w", err)
	}
	home = filepath.Clean(home)
	stored, err := pins.NewDefaultStore(paths).Load()
	if err != nil {
		return Result{}, fmt.Errorf("load pin set: %w", err)
	}
	refs := make([]pins.ProjectRef, 0, len(registry.Projects))
	for _, p := range registry.Projects {
		refs = append(refs, pins.ProjectRef{UID: p.Metadata.UID, Root: p.Spec.Root})
	}
	resolver := pins.Resolver{Projects: refs}
	repoRoot, _ := ResolveProjdir(home, s.LookupEnv, s.TmuxProjdir, config.LoadProjdir)
	inputs := DiscoveryInputs(home, repoRoot, pins.DiscoveryPaths(stored, resolver), s.LookupEnv, config.LoadWorkdirs)
	return Project(ctx, registry, inputs, pins.Select(stored, resolver))
}

// Project uses the configured discovery order and pin selection for one snapshot.
func Project(ctx context.Context, registry metadata.Registry, inputs candidates.Inputs, selection pins.Selection) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	paths, err := candidates.DiscoverProjectRoots(inputs)
	if err != nil {
		return Result{}, err
	}
	out := Result{Candidates: make([]Candidate, 0, len(paths))}
	for _, path := range paths {
		c := Candidate{Path: path, Pinned: selection.PinnedPath(path)}
		if p, ok := registry.ProjectByRoot(cleanPath(path)); ok {
			c.ProjectUID = p.Metadata.UID
		}
		out.Candidates = append(out.Candidates, c)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	return out, nil
}
