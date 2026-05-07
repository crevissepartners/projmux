package app

import (
	"os"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
)

// defaultTmuxClient builds the production tmux client wired with the optional
// post-create hook runner. The global hook path uses the standard XDG-derived
// projmux config dir. Project-local hooks can still attach a runner when the
// global path is unavailable because their discovery starts from the session
// CWD at hook time.
func defaultTmuxClient() *inttmux.Client {
	opts := []inttmux.ClientOption{}
	hookPath := defaultPostCreateHookPath()
	projectHooksEnabled := hooks.ProjectLocalPostCreateHooksEnabledFromEnv(os.Getenv)
	if runner := defaultPostCreateRunner(hookPath, projectHooksEnabled); runner != nil {
		opts = append(opts, inttmux.WithPostCreateRunner(runner))
	}
	return inttmux.NewClient(inttmux.ExecRunner{}, opts...)
}

func defaultPostCreateRunner(hookPath string, projectHooksEnabled bool) *hooks.PostCreateRunner {
	if hookPath == "" && !projectHooksEnabled {
		return nil
	}
	return &hooks.PostCreateRunner{
		HookPath:            hookPath,
		Logger:              os.Stderr,
		Timeout:             hooks.DefaultPostCreateTimeout,
		Version:             version.String(),
		ProjectHooksEnabled: projectHooksEnabled,
	}
}

func defaultPostCreateHookPath() string {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return ""
	}
	return paths.PostCreateHookPath()
}
