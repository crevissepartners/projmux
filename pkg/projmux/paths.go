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

// configPaths is the internal layout for these directories, so every file
// getter below returns the path the CLI itself reads and writes.
func (p Paths) configPaths() config.Paths {
	return config.Paths{ConfigDir: p.ConfigDir, StateDir: p.StateDir}
}

// PinFile returns the persistent pin state file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) PinFile() string { return p.configPaths().PinFile() }

// GlobalConfigFile returns the optional global config.toml, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) GlobalConfigFile() string { return p.configPaths().GlobalConfigFile() }

// AgentQuestionWindowSecondsFile returns the saved question window seconds, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) AgentQuestionWindowSecondsFile() string {
	return p.configPaths().AgentQuestionWindowSecondsFile()
}

// AgentQuestionAnsweringFile returns the saved question answering way, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) AgentQuestionAnsweringFile() string {
	return p.configPaths().AgentQuestionAnsweringFile()
}

// AgentApprovalAnsweringFile returns the saved permission answering way, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) AgentApprovalAnsweringFile() string {
	return p.configPaths().AgentApprovalAnsweringFile()
}

// AgentApprovalWindowSecondsFile returns the saved permission window seconds, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) AgentApprovalWindowSecondsFile() string {
	return p.configPaths().AgentApprovalWindowSecondsFile()
}

// TmuxAISplitModeFile returns the TUI AI split mode file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) TmuxAISplitModeFile() string { return p.configPaths().TmuxAISplitModeFile() }

// AIEnabledAgentsFile returns the enabled AI agent providers file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) AIEnabledAgentsFile() string { return p.configPaths().AIEnabledAgentsFile() }

// StatusbarNotificationsHUDVisibilityFile returns the notifications HUD visibility file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarNotificationsHUDVisibilityFile() string {
	return p.configPaths().StatusbarNotificationsHUDVisibilityFile()
}

// StatusbarAgentUsageHUDVisibilityFile returns the agent usage HUD visibility file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarAgentUsageHUDVisibilityFile() string {
	return p.configPaths().StatusbarAgentUsageHUDVisibilityFile()
}

// StatusbarGitVisibilityFile returns the git segment visibility file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarGitVisibilityFile() string {
	return p.configPaths().StatusbarGitVisibilityFile()
}

// StatusbarClockVisibilityFile returns the clock segment visibility file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarClockVisibilityFile() string {
	return p.configPaths().StatusbarClockVisibilityFile()
}

// StatusbarProjectVisibilityFile returns the project segment visibility file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarProjectVisibilityFile() string {
	return p.configPaths().StatusbarProjectVisibilityFile()
}

// StatusbarDefaultsFile returns the central statusbar defaults file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) StatusbarDefaultsFile() string { return p.configPaths().StatusbarDefaultsFile() }

// LiveResourcesFile returns the live resources toggle file, the same path the CLI uses for these directories.
// It only computes the path; the file need not exist.
func (p Paths) LiveResourcesFile() string { return p.configPaths().LiveResourcesFile() }
