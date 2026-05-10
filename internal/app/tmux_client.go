package app

import (
	"os"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
)

// defaultTmuxClient builds the production tmux client wired with the optional
// lifecycle hook runner. Hook discovery uses the standard XDG-derived projmux
// config dir; if that resolution fails we silently fall back to a hookless
// client so session creation is never blocked by config errors.
func defaultTmuxClient() *inttmux.Client {
	opts := []inttmux.ClientOption{}
	if runner := defaultLifecycleHookRunner(); runner != nil {
		opts = append(opts, inttmux.WithLifecycleHookRunner(runner))
	}
	return inttmux.NewClient(inttmux.ExecRunner{}, opts...)
}

func defaultLifecycleHookRunner() *hooks.Runner {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil
	}
	return &hooks.Runner{
		GlobalHookPaths: map[hooks.Event][]string{
			hooks.EventPreCreate:   {paths.HookPath(config.PreCreateHookFileName)},
			hooks.EventPostCreate:  {paths.HookPath(config.PostCreateHookFileName)},
			hooks.EventPaneStartup: {paths.HookPath(config.PaneStartupHookFileName)},
			hooks.EventPostAttach:  {paths.HookPath(config.PostAttachHookFileName)},
		},
		DiscoverProjectHooks: true,
		Logger:               os.Stderr,
		Timeout:              hooks.DefaultPostCreateTimeout,
		Version:              version.String(),
	}
}

func defaultPostCreateHookPath() string {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return ""
	}
	return paths.PostCreateHookPath()
}
