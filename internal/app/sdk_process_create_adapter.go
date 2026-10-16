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
	return &hooks.Runner{GlobalConfigPath: paths.GlobalConfigFile(), DiscoverProjectHooks: true, TrustStorePath: filepath.Join(paths.StateDir, "trusted-projects.json"), Timeout: hooks.DefaultPostCreateTimeout, Version: version.String()}
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

// Only the SDK supervisor receives these existing activation-routing fields.
// Its provider specification and the legacy CLI supervisor environment stay intact.
func (c *createCommand) processCreateSupervisorEnvironment(base []string, binding processhost.Binding, path string) ([]string, error) {
	if c.processCreateContext == nil {
		return base, nil
	}
	if err := mutationruntime.ValidateProcessCreateContext(*c.processCreateContext); err != nil {
		return nil, err
	}
	if path != intmetadata.PathFor(c.processCreateContext.Paths.StateDir) || binding.Pane == "" || binding.Generation == "" {
		return nil, errors.New("process-supervisor-domain-mismatch")
	}
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "PMX_INTERNAL_") {
			env = append(env, entry)
		}
	}
	return append(env, internalClaudeRegistryPathEnv+"="+path, internalActivationPaneUIDEnv+"="+binding.Pane, internalActivationGenerationEnv+"="+binding.Generation), nil
}

// The supervisor needs no Config domain. Before constructing App, admit only
// its exact private pipes and reciprocal reservation, and select that State
// domain for diagnostics. No pipe content or offset is consumed here.
func processCreateSupervisorDomain(args []string, lookup func(string) (string, bool), pipe func(int) error) (string, bool, error) {
	if len(args) < 2 || args[0] != "internal" || args[1] != "process-host-supervisor" {
		return "", false, nil
	}
	path, hasPath := lookup(internalClaudeRegistryPathEnv)
	paneUID, hasPane := lookup(internalActivationPaneUIDEnv)
	generation, hasGeneration := lookup(internalActivationGenerationEnv)
	if !hasPath && !hasPane && !hasGeneration {
		return "", false, nil
	}
	if len(args) != 2 || !hasPath || !hasPane || !hasGeneration || path == "" || paneUID == "" || generation == "" {
		return "", true, errors.New("process-supervisor-domain-incomplete")
	}
	for fd := 3; fd <= 5; fd++ {
		if err := pipe(fd); err != nil {
			return "", true, err
		}
	}
	if err := exactActivationRegistryPath(path); err != nil {
		return "", true, err
	}
	reg, err := intmetadata.NewStore(path).LoadDegradedReadOnly()
	if err != nil {
		return "", true, err
	}
	pane, ok := reg.Pane(paneUID)
	if !ok || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess || pane.Status.ProcessSession == nil {
		return "", true, errors.New("process-supervisor-pane-mismatch")
	}
	binding := pane.Status.ProcessSession.Binding
	if binding.PaneUID != paneUID || binding.Generation != generation || binding.HostInstanceID == "" || binding.OperationID == "" || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != binding.AgentUID {
		return "", true, errors.New("process-supervisor-binding-mismatch")
	}
	agent, ok := reg.Agent(binding.AgentUID)
	if !ok || agent.Status.PaneRef != paneUID || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow || agent.Metadata.OwnerRef.UID != binding.WindowUID || (agent.Status.Phase != coremetadata.PhasePending && agent.Status.Phase != coremetadata.PhaseRunning) || pane.Status.ProcessSession.Provider != agent.Spec.Provider || (agent.Spec.Provider != "claude" && agent.Spec.Provider != "codex") {
		return "", true, errors.New("process-supervisor-agent-mismatch")
	}
	window, ok := reg.Window(binding.WindowUID)
	if !ok || window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != coremetadata.KindProject || window.Metadata.OwnerRef.UID != binding.ProjectUID {
		return "", true, errors.New("process-supervisor-window-mismatch")
	}
	if _, ok := reg.Project(binding.ProjectUID); !ok {
		return "", true, errors.New("process-supervisor-project-mismatch")
	}
	for _, other := range reg.Panes {
		if other.Metadata.UID != paneUID && other.Metadata.OwnerRef != nil && other.Metadata.OwnerRef.Kind == coremetadata.KindAgent && other.Metadata.OwnerRef.UID == binding.AgentUID {
			return "", true, errors.New("process-supervisor-pane-ambiguous")
		}
	}
	for _, other := range reg.Agents {
		if other.Metadata.UID != binding.AgentUID && other.Status.PaneRef == paneUID {
			return "", true, errors.New("process-supervisor-agent-ambiguous")
		}
	}
	return filepath.Dir(filepath.Dir(path)), true, nil
}

func processCreateSupervisorPipe(fd int) error {
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return fmt.Errorf("process supervisor requires inherited pipe descriptor %d", fd)
	}
	return nil
}

// Select diagnostics only for exact owned internal entrypoints. Neither an
// inherited private variable on an ordinary CLI command nor a malformed
// explicit request may redirect or fall back to an ambient journal.
func processCreateObservationStateDir(args []string) (string, bool, error) {
	if len(args) < 2 || args[0] != "internal" {
		return "", false, nil
	}
	switch args[1] {
	case "process-host-supervisor":
		return processCreateSupervisorDomain(args, os.LookupEnv, processCreateSupervisorPipe)
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
	if request.ResumeContext != nil {
		if request.Context != nil || request.Resume == nil || request.Relaunch != nil {
			return "", true, errors.New("process-detached-context-resume-only")
		}
		frozen := *request.ResumeContext
		if err := mutationruntime.ValidateProcessCreateContext(frozen); err != nil {
			return "", true, err
		}
		if request.RegistryPath != intmetadata.PathFor(frozen.Paths.StateDir) || !request.Caller.Valid() || int64(request.Caller.OwnerUID) != int64(os.Getuid()) {
			return "", true, errors.New("process-detached-context-registry-mismatch")
		}
		if err := validateSDKProcessResumeWire(*request.Resume); err != nil {
			return "", true, err
		}
		return frozen.Paths.StateDir, true, nil
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
