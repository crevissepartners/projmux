package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
)

type projectConfigAuthorizer interface {
	AuthorizeProjectConfig(repoPath string) (bool, error)
}

type projectLayoutArtifactAuthorizer interface {
	AuthorizeProjectLayoutArtifact(repoPath, relativePath, path string, contents []byte, commands []string) (bool, error)
}

// AuthorizeProjectHooks prompts for project-local config trust before any
// session creation work starts.
func (c *Client) AuthorizeProjectHooks(ctx context.Context, cwd string) (bool, error) {
	_ = ctx
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return false, errSessionCWDRequired
	}
	if c.lifecycle == nil {
		return true, nil
	}
	authorizer, ok := c.lifecycle.(projectConfigAuthorizer)
	if !ok {
		return true, nil
	}
	ok, err := authorizer.AuthorizeProjectConfig(cwd)
	if err != nil {
		return false, fmt.Errorf("authorize project hooks for %q: %w", cwd, err)
	}
	return ok, nil
}

// AuthorizeProjectLayout gates commands from a selected project layout preset
// before session creation. The artifact already contains the exact bytes used to parse
// its in-memory preset, so authorization never needs to reopen the file.
func (c *Client) AuthorizeProjectLayout(ctx context.Context, cwd string, artifact corelayout.Artifact) (bool, error) {
	_ = ctx
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return false, errSessionCWDRequired
	}
	commands := artifact.ExecutableCommands()
	if len(commands) == 0 {
		return true, nil
	}
	if c.lifecycle == nil {
		return false, errors.New("project layout trust authorizer is not configured")
	}
	authorizer, ok := c.lifecycle.(projectLayoutArtifactAuthorizer)
	if !ok {
		return false, errors.New("project layout trust authorizer is not configured")
	}
	trusted, err := authorizer.AuthorizeProjectLayoutArtifact(
		cwd,
		artifact.RelativePath,
		artifact.Path,
		artifact.Contents,
		commands,
	)
	if err != nil {
		return false, fmt.Errorf("authorize project layout %q: %w", artifact.RelativePath, err)
	}
	return trusted, nil
}
