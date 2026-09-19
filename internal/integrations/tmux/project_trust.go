package tmux

import (
	"context"
	"fmt"
	"strings"
)

type projectConfigAuthorizer interface {
	AuthorizeProjectConfig(repoPath string) (bool, error)
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
