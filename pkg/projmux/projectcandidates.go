package projmux

import (
	"context"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	tmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	service "github.com/crevissepartners/projmux/internal/services/projectcandidates"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ListProjectCandidates reads configured candidate paths in picker discovery
// order, excluding home/current-path convenience entries. It writes neither
// pins nor saved project-root memory. HOME and discovery environment are captured
// by NewClient. An explicit SocketPath permits reading @projmux_projdir on that
// server; otherwise discovery uses environment and saved settings, never TMUX.
// Registry refusals use *Refusal; filesystem errors and cancellation are ordinary.
func (c *Client) ListProjectCandidates(ctx context.Context) (core.ProjectCandidatesResult, error) {
	s := service.Service{Paths: func() (config.Paths, error) { return c.paths, c.pathsErr }, HomeDir: func() (string, error) { return c.discoveryHome, nil }, LookupEnv: func(key string) string { return c.discoveryEnv[key] }}
	if c.runtimeSocketPath != "" {
		s.TmuxProjdir = func() string {
			bounded, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			out, err := (tmux.ExecRunner{}).Run(bounded, "tmux", "-S", c.runtimeSocketPath, "show-option", "-gqv", "@projmux_projdir")
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(out))
		}
	}
	out, err := s.List(ctx)
	result := core.ProjectCandidatesResult{Candidates: make([]core.ProjectCandidate, len(out.Candidates))}
	for i, v := range out.Candidates {
		result.Candidates[i] = core.ProjectCandidate(v)
	}
	return result, resourceRefusal(err)
}
