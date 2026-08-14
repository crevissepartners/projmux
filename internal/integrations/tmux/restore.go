package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

type projectConfigAuthorizer interface {
	AuthorizeProjectConfig(repoPath string) (bool, error)
}

type projectLayoutArtifactAuthorizer interface {
	AuthorizeProjectLayoutArtifact(repoPath, relativePath, path string, contents []byte, commands []string) (bool, error)
}

// AuthorizeProjectHooks prompts for project-local config trust before any
// session creation or snapshot replay work starts.
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

// AuthorizeProjectLayout gates commands from a selected named snapshot before
// session creation. The artifact already contains the exact bytes used to parse
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

// RestoreSessionSnapshot creates a missing project session from a saved
// snapshot while preserving the same lifecycle wrapper used by empty session
// creation: pre-create first, then replay, project env, post-create, and no
// default startup command replay for restored panes.
func (c *Client) RestoreSessionSnapshot(ctx context.Context, snap sessionstate.Snapshot, cwd, source string) error {
	if strings.TrimSpace(snap.Session) == "" {
		return errSessionNameRequired
	}
	if strings.TrimSpace(cwd) == "" {
		return errSessionCWDRequired
	}
	exists, err := c.sessionExists(ctx, snap.Session)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err := c.runPreCreate(ctx, snap.Session, cwd, "persistent"); err != nil {
		return err
	}

	c.markLifecycle(diagnostics.OperationSessionCreate)
	if _, err := sessionstate.Replay(ctx, c.runner, snap, sessionstate.ReplayOptions{FallbackCWD: cwd}); err != nil {
		if c.diagnostics != nil {
			c.diagnostics.SealFailure(diagnostics.OperationSessionCreate)
		}
		return fmt.Errorf("restore tmux session %q from snapshot: %w", snap.Session, err)
	}
	sessionEnv := c.projectSessionEnv(cwd)
	c.applyProjectSessionEnv(ctx, snap.Session, sessionEnv)
	// Snapshot replay owns its pane topology and does not expose a single
	// createDetachedSession pane id to this lifecycle boundary.
	c.runPostCreate(ctx, snap.Session, cwd, "persistent", "")
	if err := c.MarkSessionStateSource(ctx, snap.Session, source); err != nil {
		return err
	}
	return nil
}
