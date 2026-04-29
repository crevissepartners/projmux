package app

import (
	"os"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/version"
)

// defaultTmuxClient builds the production tmux client wired with the optional
// post-create hook runner. Hook discovery uses the standard XDG-derived
// projmux config dir; if that resolution fails we silently fall back to a
// hookless client so session creation is never blocked by config errors.
func defaultTmuxClient() *inttmux.Client {
	opts := []inttmux.ClientOption{}
	if hookPath := defaultPostCreateHookPath(); hookPath != "" {
		opts = append(opts, inttmux.WithPostCreateRunner(&hooks.PostCreateRunner{
			HookPath: hookPath,
			Logger:   os.Stderr,
			Timeout:  hooks.DefaultPostCreateTimeout,
			Version:  version.String(),
		}))
	}
	return inttmux.NewClient(inttmux.ExecRunner{}, opts...)
}

func defaultPostCreateHookPath() string {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return ""
	}
	return paths.PostCreateHookPath()
}
