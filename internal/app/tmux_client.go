package app

import (
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
)

// defaultTmuxClient builds the production tmux client wired with the optional
// lifecycle hook runner. Hook discovery uses the standard XDG-derived projmux
// config dir; if that resolution fails we silently fall back to a hookless
// client so session creation is never blocked by config errors.
func defaultTmuxClient(recorders ...*diagnostics.LifecycleRecorder) *inttmux.Client {
	opts := []inttmux.ClientOption{inttmux.WithSocketName(defaultAppSocket)}
	if len(recorders) > 0 && recorders[0] != nil {
		opts = append(opts, inttmux.WithLifecycleDiagnostics(recorders[0]))
	}
	if runner := defaultLifecycleHookRunner(); runner != nil {
		opts = append(opts, inttmux.WithLifecycleHookRunner(runner))
	}
	return inttmux.NewClient(inttmux.ExecRunner{}, opts...)
}

func defaultLifecycleHookRunner() *hooks.Runner {
	globalConfigPath, err := hooks.GlobalConfigPath(os.Getenv, os.UserHomeDir)
	if err != nil {
		return nil
	}

	prompt := hooks.ProjectHookPrompt(nil)
	if strings.TrimSpace(os.Getenv("TMUX")) != "" && strings.TrimSpace(os.Getenv(hookTrustInlineEnv)) == "" {
		prompt = tmuxProjectHookPrompt(os.Getenv, rawExecutablePath, inttmux.ExecRunner{})
	}
	return &hooks.Runner{
		GlobalConfigPath:     globalConfigPath,
		DiscoverProjectHooks: true,
		ProjectHookPrompt:    prompt,
		Logger:               os.Stderr,
		Timeout:              hooks.DefaultPostCreateTimeout,
		Version:              version.String(),
	}
}
