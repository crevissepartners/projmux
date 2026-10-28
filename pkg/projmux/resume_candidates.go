package projmux

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ListResumeCandidates lists the recorded sessions of query's directory with
// the discovery the CLI resume picker runs: the same providers, scan depth,
// row limit, per-provider budgets, Codex catalog routes and collision rules,
// so the same inputs give the same candidates in the same order. Reading the
// Codex catalog may start the default Codex app-server proxy, as the picker
// does. A provider whose search failed or did not finish contributes no rows.
// Each call runs a discovery of its own and shares no state with another
// call, so concurrent calls are independent. The Codex catalog is read with
// the Client's captured home and environment; when that native session
// cannot be prepared, Codex rows come from rollouts only.
func (c *Client) ListResumeCandidates(ctx context.Context, query core.ResumeCandidateQuery) ([]core.ResumeCandidate, error) {
	return c.listResumeCandidates(ctx, query, true)
}

// listResumeCandidates lists query's candidates. Without catalog the Codex
// app-server is never read or started: Codex rows come from rollouts only.
func (c *Client) listResumeCandidates(ctx context.Context, query core.ResumeCandidateQuery, catalog bool) ([]core.ResumeCandidate, error) {
	cwd, enabled, err := c.resumeCandidateInputs(ctx, query)
	if err != nil {
		return nil, err
	}
	frame := c.resumeDiscovery(ctx, cwd, enabled, catalog).Populate()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates := make([]core.ResumeCandidate, 0, len(frame.Summaries))
	for _, summary := range frame.Summaries {
		candidates = append(candidates, core.ResumeCandidate{
			Provider: summary.Provider, Conversation: summary.ResumeID, Source: summary.Source, Cwd: resumeCandidateCwd(cwd, summary.RelativeCWD),
			Title: summary.Label, Branch: summary.Branch, UpdatedAt: summary.UpdatedAt,
		})
	}
	return candidates, nil
}

// resumeCandidateInputs resolves the directory and the providers of query.
func (c *Client) resumeCandidateInputs(ctx context.Context, query core.ResumeCandidateQuery) (string, map[string]bool, error) {
	scope, _, _, err := c.createInputs(ctx, "ResumeCandidateQuery", query.Project, "", nil, "")
	if err != nil {
		return "", nil, err
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return "", nil, resourceRefusal(err)
	}
	execution, _ := mutationruntime.NewSDKCreate(c.paths, "", c.projectShell, c.projmuxExecutable, c.projectSessionName, tmux.ExecRunner{}, c.createDiscoveryRoots)
	project, err := execution.ResolveProject(reg, scope)
	if err != nil {
		return "", nil, createRefusal(err, "ResumeCandidateQuery")
	}
	cwd := project.Spec.Root
	if query.Cwd != "" {
		if !filepath.IsAbs(query.Cwd) {
			return "", nil, &Refusal{Code: CodeCreateRequestInvalid, message: "ResumeCandidateQuery.Cwd must be absolute"}
		}
		cwd = filepath.Clean(query.Cwd)
	}
	providers, err := config.LoadAIEnabledAgentsFile(c.paths.AIEnabledAgentsFile())
	if err != nil {
		providers = config.DefaultAIEnabledAgents
	}
	want := strings.TrimSpace(query.Provider)
	if want != "" && want != "claude" && want != "codex" && want != "antigravity" {
		return "", nil, &Refusal{Code: CodeCreateRequestInvalid, message: "ResumeCandidateQuery.Provider requires claude, codex or antigravity"}
	}
	enabled := map[string]bool{}
	for _, provider := range providers {
		if name := string(provider); want == "" || name == want {
			enabled[name] = true
		}
	}
	return cwd, enabled, ctx.Err()
}

// resumeDiscovery is a new discovery of cwd for one call, read through the
// Client's home and environment. With catalog, the default Codex app-server
// route is found and connected through the Client's captured native session.
func (c *Client) resumeDiscovery(ctx context.Context, cwd string, enabled map[string]bool, catalog bool) *mutationruntime.ResumeDiscovery {
	home := func() (string, error) { return c.discoveryHome, nil }
	lookup := func(key string) string { return c.discoveryEnv[key] }
	opts := mutationruntime.ResumeDiscoveryOptions{
		Ctx: ctx, Cwd: cwd, Home: c.discoveryHome, ProviderEnabled: enabled,
		Depth:             mutationruntime.ResolveResumeScanDepth(home, lookup, cwd).Depth,
		Limit:             mutationruntime.ResolveResumePickerLimit(home, lookup, cwd).Limit,
		PopulationTimeout: mutationruntime.ResumeSummaryPopulationBudget, PopulationContext: context.WithTimeout,
		Budgets:      mutationruntime.DefaultResumeBudgets(),
		LoadRegistry: func() (metadata.Registry, error) { return resources.Load(c.paths) },
		Discover:     aisessions.DiscoverResumeSummariesContext,
	}
	if catalog {
		planner := &mutationruntime.SDKAgentPlanner{Paths: c.paths, HomeDir: c.discoveryHome, Shell: c.projectShell, Executable: c.projmuxExecutable, Environment: c.agentEnvironment, Store: &mutationruntime.CreateStore{Load: opts.LoadRegistry}}
		planner.Initialize()
		if native, err := planner.NativeSession(); err == nil {
			opts.CodexNative = sdkCodexCatalog{current: native.Current}
			opts.OpenRoute = func(ctx context.Context, route mutationruntime.NativeCodexRoute, experimental bool) (*codexappserver.Client, error) {
				if route.Default {
					return native.Open(ctx, experimental)
				}
				return mutationruntime.OpenNativeCodexRoute(ctx, route, experimental)
			}
			opts.OpenCodexCatalog = func(ctx context.Context) (aisessions.CodexCatalog, error) {
				client, err := native.Open(ctx, false)
				if err != nil {
					return nil, err
				}
				return aisessions.NewCodexCatalog(client), nil
			}
		}
	}
	if c.resumeSources != nil {
		c.resumeSources(&opts)
	}
	if !catalog {
		opts.CodexNative, opts.OpenRoute, opts.OpenCodexCatalog = nil, nil, nil
	}
	return mutationruntime.NewResumeDiscovery(opts)
}

// sdkCodexCatalog is the one current generation of the default Codex
// app-server, the inventory the CLI's default controller reads.
type sdkCodexCatalog struct {
	current func(context.Context) (mutationruntime.NativeCodexRoute, error)
}

func (c sdkCodexCatalog) CatalogRoutes(ctx context.Context) ([]mutationruntime.NativeCodexRoute, error) {
	route, err := c.current(ctx)
	if err != nil {
		return nil, err
	}
	return []mutationruntime.NativeCodexRoute{route}, nil
}

// resumeCandidateCwd is the absolute directory of a row the picker shows
// relative to base: "./" is base itself and no relative path is base too.
func resumeCandidateCwd(base, relative string) string {
	if after, ok := strings.CutPrefix(strings.TrimSpace(relative), "./"); ok && after != "" {
		return filepath.Join(base, after)
	}
	return base
}
