package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/version"
)

func (c *aiCommand) observerConfigPaths() (config.Paths, error) {
	if c.nativeObserverPaths != nil {
		return c.nativeObserverPaths.Paths, c.nativeObserverPaths.Validate()
	}
	return configPaths(c.homeDir, c.lookupEnv)
}

// withNativeObserverPaths binds every observer reader/writer together without
// changing the caller's command or process environment. Partial tuples refuse;
// a Registry file need not be underneath the State directory.
func (c *aiCommand) withNativeObserverPaths(paths mutationruntime.NativeObserverPaths) (*aiCommand, error) {
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	child := c.cloneSeams()
	child.nativeObserverPaths = &paths
	child.executable = func() (string, error) { return paths.Executable, nil }
	store := intmetadata.NewStore(paths.RegistryPath)
	child.loadRegistry = store.LoadReadOnly
	child.updateRegistry = store.Update
	child.acquireCodexAuthority = nil
	child.notifyStore = notify.NewDefaultStore(paths.Paths)
	child.events = newNotifyQueueRefreshTransport(paths.Paths.StateDir)
	recorder := diagnostics.NewLifecycleRecorder(diagnostics.NewStore(filepath.Join(paths.Paths.StateDir, diagnostics.LogDirName, diagnostics.LogFileName)), diagnostics.NewRunID(), version.String(), "tmux")
	child.operationalDiagnostics = recorder.AI()
	child.notifyDiagnostics = recorder.NotifyFocus()
	runner := &hooks.Runner{GlobalConfigPath: paths.Paths.GlobalConfigFile(), DiscoverProjectHooks: true, ProjectHookPrompt: hooks.ProjectHookPrompt(nil), Logger: os.Stderr, Timeout: hooks.DefaultPostCreateTimeout, Version: version.String()}
	child.producer = &storeAttentionNotifyProducer{store: child.notifyStore, ttl: attentionNotifyTTL,
		events: child.events, diagnostics: child.notifyDiagnostics,
		hooks: &sendNotiHookDispatcher{runner: runner, lookupEnv: child.lookupEnv, getwd: os.Getwd},
	}
	return child, nil
}

// Keep the provider question/notification hook config explicit too.
func (c *aiCommand) observerGlobalConfig() (hooks.ProjectConfig, error) {
	paths, err := c.observerConfigPaths()
	if err != nil {
		return hooks.ProjectConfig{}, err
	}
	return hooks.LoadGlobalConfig(paths.GlobalConfigFile())
}

// NativeObservationStateDir routes the executable's outer diagnostics and
// migration adapter before any observer/broker child code is entered. Invalid
// explicit inputs are handled too: they must not fall back to a default journal.
// This is hidden CLI plumbing, not a public SDK operation.
func NativeObservationStateDir(args []string) (stateDir string, explicit bool, err error) {
	if state, explicit, err := processCreateObservationStateDir(args); explicit {
		return state, explicit, err
	}
	if len(args) >= 4 && args[0] == "internal" && args[1] == "agent-hook" && args[2] == "ingest" && args[3] == codexNativeLifecycleIngestRoute {
		explicit = nativeObserverPathFlagsPresent(args[4:])
		if !explicit {
			return "", false, nil
		}
		target, err := parseCodexNativeLifecycleTarget(args[4:])
		if err != nil {
			return "", true, err
		}
		if target.Paths == nil {
			return "", true, fmt.Errorf("native observation requires explicit paths")
		}
		return target.Paths.Paths.StateDir, true, target.Paths.Validate()
	}
	if len(args) >= 2 && args[0] == "internal" && args[1] == claudeQuestionPickerRoute && nativeObserverPathFlagsPresent(args[2:]) {
		input, err := parseClaudeQuestionPickerInput(args[2:], io.Discard)
		if err != nil {
			return "", true, err
		}
		return input.Paths.Paths.StateDir, true, input.Paths.Validate()
	}

	if len(args) >= 3 && args[0] == "internal" && args[1] == "codex-broker" && args[2] == "serve" {
		input, err := parseCodexBrokerServeInput(args[3:], io.Discard)
		if err != nil {
			return "", true, err
		}
		if input.Explicit {
			return filepath.Clean(input.StateDomain), true, nil
		}
	}
	return "", false, nil
}

func nativeObserverPathArgs(paths mutationruntime.NativeObserverPaths) []string {
	return []string{"--config-dir", paths.Paths.ConfigDir, "--state-dir", paths.Paths.StateDir, "--registry-path", paths.RegistryPath, "--projmux-executable", paths.Executable}
}

func nativeObserverPathFlagsPresent(args []string) bool {
	for _, arg := range args {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--config-dir", "-config-dir", "--state-dir", "-state-dir", "--registry-path", "-registry-path", "--projmux-executable", "-projmux-executable":
			return true
		}
	}
	return false
}
