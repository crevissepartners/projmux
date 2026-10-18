package projmux

import (
	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// ErrHomeDirRequired identifies a missing HOME fallback when an XDG home is
// not absolute. Use errors.Is; resolution errors retain the CLI's wording.
var ErrHomeDirRequired = config.ErrHomeDirRequired

// ResolvePathsOptions supplies inputs for path resolution, independently of
// Options.HomeDir (which overrides session naming only).
type ResolvePathsOptions struct {
	// HomeDir defaults to os.UserHomeDir when nil. A callback returning an empty
	// string supplies a missing home; relative homes retain the CLI's behavior.
	HomeDir func() (string, error)
	// LookupEnv defaults to os.Getenv when nil. Only XDG_CONFIG_HOME and
	// XDG_STATE_HOME are requested, in that order, after HomeDir succeeds.
	LookupEnv func(string) string
}

// Paths contains the resolved projmux directories, including the projmux suffix.
// Resolution does not guarantee that either directory exists or is writable.
type Paths struct {
	ConfigDir string
	StateDir  string
}

// ResolvePaths resolves the same HOME/XDG paths as the CLI without constructing
// a Client, reading files, creating directories, or changing the environment.
// It calls HomeDir first, collects both XDG inputs, then resolves config before
// state. Blank or relative XDG values (including literal tilde paths) are unset.
// Any error returns zero Paths and preserves the original cause. Effects of
// caller-supplied callbacks are the caller's responsibility.
func ResolvePaths(options ResolvePathsOptions) (Paths, error) {
	paths, err := mutationruntime.ConfigPaths(options.HomeDir, options.LookupEnv)
	if err != nil {
		return Paths{}, err
	}
	return Paths{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir}, nil
}
