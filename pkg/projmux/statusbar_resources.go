package projmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/settings"
	"github.com/crevissepartners/projmux/internal/services/tmuxconfig"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeStatusbarResourcesUnsupported   Code = "statusbar-resources-unsupported"
	CodeStatusbarResourcesSaveFailed    Code = "statusbar-resources-save-failed"
	CodeStatusbarResourcesPrepareFailed Code = "statusbar-resources-prepare-failed"
	CodeStatusbarResourcesApplyFailed   Code = "statusbar-resources-apply-failed"
)

// SetStatusbarResources validates trimmed exact on/off, saves the preference,
// generates the complete tmux config, then applies it to the exact owned app
// server. It uses captured paths/home/shell/helper/transport, ignoring ambient
// TMUX, TMUX_PANE and private anchors. ProjmuxExecutable must explicitly name an
// absolute executable of the same build; helper errors occur after saving.
// This synchronous legacy operation takes no context and observes no cancellation.
// An error retains the partial result and wraps the original cause. Concurrent
// settings saves are not serialized and failure does not roll back completed effects.
func (c *Client) SetStatusbarResources(value string) (core.StatusbarResourcesApplyResult, error) {
	return c.setStatusbarResources(value, ResourcesSupported, frozenTurnRunner{c})
}

func (c *Client) setStatusbarResources(value string, supported func() bool, runner mutationruntime.TmuxCommandRunner) (core.StatusbarResourcesApplyResult, error) {
	mode := strings.TrimSpace(value)
	initial := core.StatusbarResourcesApplyResult{Mode: mode, Prepared: core.StatusbarResourcesApplyStage{Status: "skipped"}, Live: core.StatusbarResourcesApplyStage{Status: "skipped"}}
	if mode != "on" && mode != "off" {
		return initial, &Refusal{Code: CodeConfigValueInvalid, message: fmt.Sprintf("invalid statusbar resources value %q", value)}
	}
	if c.transport.Kind == mutationruntime.TmuxSocketPath {
		runner = statusbarResourcesBoundRunner{Runner: runner, SocketPath: c.transport.Value}
	}
	var paths config.Paths
	apply := settings.StatusbarResourcesApply{
		Runner: runner,
		Generate: func() (string, error) {
			if err := statusbarResourcesHelper(c.projmuxExecutable); err != nil {
				return "", err
			}
			generation := tmuxconfig.Generation{
				HomeDir: func() (string, error) { return c.discoveryHome, nil },
				LookupEnv: func(key string) string {
					if key == "SHELL" {
						return c.projectShell
					}
					return ""
				},
				ResolvePaths: func(func() (string, error), func(string) string) (config.Paths, error) { return paths, nil },
				KeymapPaths:  func() (string, error) { return paths.KeymapFile(), nil },
				ReadFile:     os.ReadFile,
			}
			return tmuxconfig.WriteTmuxAppConfig(filepath.Join(paths.ConfigDir, "tmux.conf"), c.projmuxExecutable, generation, os.WriteFile)
		},
		ResolveRoute: func(ctx context.Context) (mutationruntime.RuntimeMutationRoute, error) {
			route, err := mutationruntime.ResolveExistingRuntimeMutationRoute(ctx, runner, c.transport, nil)
			if err == nil && c.transport.Kind == mutationruntime.TmuxSocketPath && route.ExpectedSocketPath != c.transport.Value {
				return mutationruntime.RuntimeMutationRoute{}, errors.New("statusbar resources route does not match captured socket path")
			}
			return route, err
		},
	}
	result, err := (settings.StatusbarResources{
		Supported: supported,
		Paths: func() (config.Paths, error) {
			var err error
			paths, err = c.instructions.Paths()
			return paths, err
		},
		Apply: apply.Apply,
	}).Set(mode)
	out := core.StatusbarResourcesApplyResult{Mode: result.Mode, Saved: result.Saved,
		Prepared: core.StatusbarResourcesApplyStage{Status: string(result.Prepared.Status), Detail: result.Prepared.Detail},
		Live:     core.StatusbarResourcesApplyStage{Status: string(result.Live.Status), Detail: result.Live.Detail}}
	if err == nil {
		return out, nil
	}
	code := CodeStatusbarResourcesApplyFailed
	switch result.Failure {
	case "unsupported":
		code = CodeStatusbarResourcesUnsupported
	case "save":
		code = CodeStatusbarResourcesSaveFailed
	case "prepare":
		code = CodeStatusbarResourcesPrepareFailed
	case "apply":
		code = CodeStatusbarResourcesApplyFailed
	}
	return out, &Refusal{Code: code, message: err.Error(), cause: err}
}

func statusbarResourcesHelper(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("statusbar resources helper must be an explicit absolute same-build executable")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("statusbar resources helper: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return errors.New("statusbar resources helper must be a regular executable file")
	}
	return nil
}

type statusbarResourcesBoundRunner struct {
	Runner     mutationruntime.TmuxCommandRunner
	SocketPath string
}

func (r statusbarResourcesBoundRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.SocketPath || strings.HasPrefix(args[2], "-") {
		return nil, errors.New("statusbar resources transport refuses a request outside the captured socket path")
	}
	if r.Runner == nil {
		return nil, errors.New("statusbar resources tmux runner is not configured")
	}
	return r.Runner.Run(ctx, name, args...)
}
