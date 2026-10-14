package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	receiptcore "github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/version"
)

// An explicit SDK context is invocation-local. The launcher already carries
// its frozen environment; these callbacks also preserve arbitrary Paths that
// cannot be represented by an XDG parent plus the fixed projmux basename.
func (c *createCommand) bindProcessCreateContext(frozen mutationruntime.ProcessCreateContext) error {
	if err := mutationruntime.ValidateProcessCreateContext(frozen); err != nil {
		return err
	}
	current, err := os.Executable()
	if err != nil {
		return err
	}
	want, err := os.Stat(frozen.Executable)
	if err != nil {
		return err
	}
	actual, err := os.Stat(current)
	if err != nil || !os.SameFile(want, actual) {
		return errors.New("process-detached-executable-mismatch")
	}
	frozen.Environment = slices.Clone(frozen.Environment)
	c.processCreateContext = &frozen
	lookup := func(key string) string {
		for _, entry := range frozen.Environment {
			k, v, _ := strings.Cut(entry, "=")
			if k == key {
				return v
			}
		}
		return ""
	}
	c.homeDir = func() (string, error) { return frozen.HomeDir, nil }
	c.lookupEnv = lookup
	storage := intmetadata.NewStore(intmetadata.PathFor(frozen.Paths.StateDir))
	c.store = &resourceStore{load: storage.LoadReadOnly, snapshot: storage.LoadReadOnly, update: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		reg, _, err := storage.UpdateConvergent(fn)
		return reg, err
	}, updateConvergent: storage.UpdateConvergent, mutator: intmetadata.DefaultMutator, stateDir: func() (string, error) { return frozen.Paths.StateDir, nil }}
	ai, ok := c.agents.(*aiCommand)
	if !ok {
		return errors.New("process-detached-launch-adapter-unavailable")
	}
	ai.processCreateContext = &frozen
	ai.homeDir, ai.lookupEnv = c.homeDir, lookup
	ai.executable = func() (string, error) { return frozen.Executable, nil }
	return nil
}

func (c *createCommand) processCreatePaths() (config.Paths, error) {
	if c.processCreateContext != nil {
		return c.processCreateContext.Paths, nil
	}
	return configPaths(c.homeDir, c.lookupEnv)
}
func (c *aiCommand) processCreatePaths() (config.Paths, error) {
	if c.processCreateContext != nil {
		return c.processCreateContext.Paths, nil
	}
	return configPaths(c.homeDir, c.lookupEnv)
}
func (c *createCommand) processCreateSettingsPaths() (config.Paths, error) {
	if c.processCreateContext != nil {
		return c.processCreateContext.Paths, nil
	}
	return savedSettingsPaths(c.homeDir, c.lookupEnv)
}
func (c *aiCommand) processCreateEnvironment() []string {
	if c.processCreateContext != nil {
		return slices.Clone(c.processCreateContext.Environment)
	}
	return os.Environ()
}
func (c *createCommand) processCreateExecutable() (string, error) {
	if c.processCreateContext != nil {
		return c.processCreateContext.Executable, nil
	}
	return os.Executable()
}
func (c *createCommand) prepareProcessPersona(name, option string) (personaLaunch, error) {
	if c.processCreateContext == nil {
		return c.preparePersonaLaunch(canonicalCreateAgent, name, option)
	}
	p, err := c.sharedAgentSettings(resourceCreateFlags{}).PreparePersonaLaunch(canonicalCreateAgent, name, option)
	return personaLaunch{name: p.Name, snapshot: p.Snapshot, content: p.Content}, err
}
func (c *createCommand) processCreateHookRunner() *hooks.Runner {
	if c.processCreateContext == nil {
		return defaultLifecycleHookRunner()
	}
	paths := c.processCreateContext.Paths
	return &hooks.Runner{GlobalConfigPath: filepath.Join(paths.ConfigDir, "config.toml"), DiscoverProjectHooks: true, TrustStorePath: filepath.Join(paths.StateDir, "trusted-projects.json"), Timeout: hooks.DefaultPostCreateTimeout, Version: version.String()}
}
func (c *createCommand) processCreateQuestionWindow(paths config.Paths) time.Duration {
	if c.processCreateContext == nil {
		return time.Duration(loadCentralAgentQuestionWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second
	}
	value, _ := config.LoadAgentQuestionWindowSecondsFile(paths.AgentQuestionWindowSecondsFile())
	return time.Duration(value) * time.Second
}
func (c *createCommand) processCreateApprovalWindow(paths config.Paths) time.Duration {
	if c.processCreateContext == nil {
		return time.Duration(loadCentralAgentApprovalWindowSeconds(c.homeDir, c.lookupEnv)) * time.Second
	}
	value, _ := config.LoadAgentApprovalWindowSecondsFile(paths.AgentApprovalWindowSecondsFile())
	return time.Duration(value) * time.Second
}
func (c *createCommand) processCreateCommitted(result processAgentCreateResult) (mutationruntime.ProcessCreateCommitted, error) {
	reg, err := c.store.snapshot()
	if err != nil {
		return mutationruntime.ProcessCreateCommitted{}, err
	}
	agent, ok := reg.Agent(result.Binding.Agent)
	if !ok {
		return mutationruntime.ProcessCreateCommitted{}, errors.New("process-detached-agent-missing")
	}
	pane, ok := reg.Pane(result.Binding.Pane)
	if !ok {
		return mutationruntime.ProcessCreateCommitted{}, errors.New("process-detached-pane-missing")
	}
	receipt := createResultsReceipt(coremetadata.KindAgent, []createResult{result.Created})
	return mutationruntime.ProcessCreateCommitted{Agent: *agent, Pane: *pane, Receipt: receiptcore.OperationReceipt(receipt), Notices: slices.Clone(result.Notices)}, nil
}

// Select diagnostics only for exact owned internal entrypoints. Neither an
// inherited private variable on an ordinary CLI command nor a malformed
// explicit request may redirect or fall back to an ambient journal.
func processCreateObservationStateDir(args []string) (string, bool, error) {
	if len(args) < 2 || args[0] != "internal" {
		return "", false, nil
	}
	switch args[1] {
	case "process-agent-launch", "process-agent-owner":
		if len(args) != 2 {
			return "", true, errors.New("process-detached-entry-arguments-invalid")
		}
		descriptor, err := syscall.Dup(4)
		if err != nil {
			return "", true, err
		}
		payload := os.NewFile(uintptr(descriptor), "process-create-context-observation")
		defer payload.Close()
		return processCreateObservationStatePayload(payload)
	case "claude-endpoint-register", claudeEndpointHelperRoute, "claude-question-hook", "claude-permission-hook":
		return processCreateHookObservationState(os.Getenv)
	}
	return "", false, nil
}

func processCreateObservationStatePayload(payload *os.File) (string, bool, error) {
	info, err := payload.Stat()
	if err != nil {
		return "", true, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > mutationruntime.ProcessDetachedWireLimit {
		return "", true, errors.New("process-detached-request-invalid")
	}
	data := make([]byte, int(info.Size()))
	if _, err := payload.ReadAt(data, 0); err != nil {
		return "", true, err
	}
	var request processDetachedRequest
	reader := json.NewDecoder(strings.NewReader(string(data)))
	reader.DisallowUnknownFields()
	if err := reader.Decode(&request); err != nil {
		return "", true, err
	}
	if err := reader.Decode(new(any)); !errors.Is(err, io.EOF) {
		return "", true, errors.New("process-detached-request-trailing-data")
	}
	if request.Context == nil {
		return "", false, nil
	}
	if request.Resume != nil || request.Relaunch != nil {
		return "", true, errors.New("process-detached-context-create-only")
	}
	if err := mutationruntime.ValidateProcessCreateContext(*request.Context); err != nil {
		return "", true, err
	}
	if request.RegistryPath != intmetadata.PathFor(request.Context.Paths.StateDir) || !request.Caller.Valid() || int64(request.Caller.OwnerUID) != int64(os.Getuid()) {
		return "", true, errors.New("process-detached-context-registry-mismatch")
	}
	if _, err := mutationruntime.NewProcessAgentCreateRequest(request.Options); err != nil {
		return "", true, err
	}
	return request.Context.Paths.StateDir, true, nil
}

func processCreateHookObservationState(lookup func(string) string) (string, bool, error) {
	raw, host := lookup(internalClaudeProcessBindingEnv), lookup(internalClaudeProcessHostEnv)
	if raw == "" && host == "" {
		return "", false, nil
	}
	path := lookup(internalClaudeRegistryPathEnv)
	if err := exactActivationRegistryPath(path); err != nil {
		return "", true, err
	}
	if raw == "" || host == "" || !filepath.IsAbs(host) {
		return "", true, errors.New("process-hook-context-incomplete")
	}
	var binding processhost.Binding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return "", true, err
	}
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return "", true, err
	}
	pane, ok := reg.Pane(binding.Pane)
	if !ok || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != metadataProcessBinding(binding) {
		return "", true, fmt.Errorf("process-hook-context-binding-mismatch")
	}
	return filepath.Dir(filepath.Dir(path)), true, nil
}
