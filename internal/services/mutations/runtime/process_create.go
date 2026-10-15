package runtime

import (
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ProcessCreateContext freezes the caller-selected domain and child environment.
// Nil context on CLI requests preserves its existing invocation environment.
type ProcessCreateContext struct {
	Paths               config.Paths
	HomeDir, Executable string
	Environment         []string
}

func processCreateUsage(message string) error { return &createUsageError{message: message} }

// Typed callers and the CLI share this validated request. It contains no argv
// parser, output selection, terminal handle, or inferred runtime authority.
type ProcessAgentCreateOptions struct {
	Project, Window selector.Ref
	// NewWindow requests shell-free creation. A matching Window reference ensures
	// an existing exact name; an absent Window reference always creates a new one.
	NewWindow                                                          *coremetadata.BootstrapWindow
	Provider, Name, CWD, Model, Effort, Instructions, Profile, Persona string
	// Creator is the bare Agent UID validated by parseResourceCreateFlags.
	Creator          string
	Payload, AddDirs []string
	Labels           map[string]string
}

type ProcessAgentCreateRequest struct{ Options ProcessAgentCreateOptions }

func NewProcessAgentCreateRequest(opts ProcessAgentCreateOptions) (ProcessAgentCreateRequest, error) {
	if err := validateProcessCreateRef(opts.Project, coremetadata.KindProject); err != nil {
		return ProcessAgentCreateRequest{}, err
	}
	if err := validateProcessCreateRef(opts.Window, coremetadata.KindWindow); err != nil {
		return ProcessAgentCreateRequest{}, err
	}
	if opts.Project == (selector.Ref{}) && opts.Window == (selector.Ref{}) {
		return ProcessAgentCreateRequest{}, processCreateUsage("create agent --host process requires an exact Project or Window scope")
	}
	if opts.NewWindow != nil {
		if opts.Project == (selector.Ref{}) || opts.Window.UID != "" || (opts.Window.Name != "" && opts.Window.Name != opts.NewWindow.Name) {
			return ProcessAgentCreateRequest{}, processCreateUsage("process-create-window-scope-invalid: a new Window requires an exact Project and a matching Window name")
		}
		if opts.NewWindow.Name != "" {
			if err := coremetadata.ValidateName(opts.NewWindow.Name); err != nil {
				return ProcessAgentCreateRequest{}, MapMetadataError(err)
			}
		}
		window := *opts.NewWindow
		window.Labels = maps.Clone(window.Labels)
		window.Panes = nil
		opts.NewWindow = &window
	}
	if opts.Name != "" {
		if err := coremetadata.ValidateName(opts.Name); err != nil {
			return ProcessAgentCreateRequest{}, MapMetadataError(err)
		}
	}
	if opts.Instructions != "" && opts.Persona != "" {
		return ProcessAgentCreateRequest{}, processCreateUsage("create agent accepts only one of --instructions and --persona")
	}
	if opts.Provider != "" && !slices.Contains([]string{"claude", "codex", "antigravity"}, opts.Provider) {
		return ProcessAgentCreateRequest{}, processCreateUsage("create agent: unknown Agent provider")
	}
	// This typed seam receives a normalized UID, not argv syntax. Reject an
	// old in-process caller's uid: prefix without parsing it a second time.
	if opts.Creator != "" && (strings.TrimSpace(opts.Creator) != opts.Creator || strings.ContainsAny(opts.Creator, ":/ \t\r\n")) {
		return ProcessAgentCreateRequest{}, processCreateUsage("create agent process-creator-uid-invalid: expected a bare Agent UID; nothing was created")
	}
	opts.Payload, opts.AddDirs, opts.Labels = slices.Clone(opts.Payload), slices.Clone(opts.AddDirs), maps.Clone(opts.Labels)
	return ProcessAgentCreateRequest{Options: opts}, nil
}

func validateProcessCreateRef(ref selector.Ref, kind coremetadata.Kind) error {
	if ref == (selector.Ref{}) {
		return nil
	}
	if ref.Kind != kind || (ref.UID == "") == (ref.Name == "") {
		return processCreateUsage(fmt.Sprintf("create agent: %s requires one typed name or UID reference", kind))
	}
	raw := ref.Name
	if ref.UID != "" {
		raw = selector.UIDPrefix + ref.UID
	}
	parsed, err := selector.ParseRef(kind, raw)
	if err != nil {
		return MapMetadataError(err)
	}
	if parsed.UID != ref.UID || parsed.Name != ref.Name {
		return processCreateUsage(fmt.Sprintf("create agent: %s typed reference changes meaning when parsed", kind))
	}
	return nil
}

// CaptureProcessCreateContext reads launch defaults once at client construction.
// The explicit HOME and roots replace ambient routing; no operation reads env.
func CaptureProcessCreateContext(paths config.Paths, home, executable, shell string) ProcessCreateContext {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "TMUX" || key == "TMUX_PANE" || strings.HasPrefix(key, "__PROJMUX_") || strings.HasPrefix(key, "PMX_INTERNAL_") {
			continue
		}
		values[key] = value
	}
	values["HOME"] = home
	values["SHELL"] = shell
	values["XDG_CONFIG_HOME"] = filepath.Dir(paths.ConfigDir)
	values["XDG_STATE_HOME"] = filepath.Dir(paths.StateDir)
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	slices.Sort(env)
	return ProcessCreateContext{Paths: paths, HomeDir: home, Executable: executable, Environment: env}
}

// ValidateProcessCreateContext is read-only and does not infer a missing root.
func ValidateProcessCreateContext(frozen ProcessCreateContext) error {
	for _, pair := range []struct{ name, path string }{{"HomeDir", frozen.HomeDir}, {"ConfigDir", frozen.Paths.ConfigDir}, {"StateDir", frozen.Paths.StateDir}, {"ProjmuxExecutable", frozen.Executable}} {
		if !filepath.IsAbs(pair.path) {
			return &AgentCreateRefusal{Reason: "create-request-invalid", Message: pair.name + " must be absolute"}
		}
	}
	if frozen.Environment == nil {
		return &AgentCreateRefusal{Reason: "create-request-invalid", Message: "process create requires a frozen environment"}
	}
	values := map[string]string{}
	for _, entry := range frozen.Environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(entry, "\x00") {
			return &AgentCreateRefusal{Reason: "create-request-invalid", Message: "process create environment is invalid"}
		}
		if _, exists := values[key]; exists {
			return &AgentCreateRefusal{Reason: "create-request-invalid", Message: "process create environment contains duplicate keys"}
		}
		values[key] = value
	}
	if values["HOME"] != frozen.HomeDir || values["XDG_CONFIG_HOME"] != filepath.Dir(frozen.Paths.ConfigDir) || values["XDG_STATE_HOME"] != filepath.Dir(frozen.Paths.StateDir) {
		return &AgentCreateRefusal{Reason: "create-request-invalid", Message: "process create environment and explicit roots differ"}
	}
	info, err := os.Stat(frozen.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return &AgentCreateRefusal{Reason: "create-executable-invalid", Message: "Options.ProjmuxExecutable must name an existing executable file", Cause: err}
	}
	return nil
}

func ResolveProcessCreateScope(reg coremetadata.Registry, opts ProcessAgentCreateOptions) (coremetadata.Project, coremetadata.Window, error) {
	var project coremetadata.Project
	query := selector.Query{}
	if opts.NewWindow != nil {
		matches, err := selector.New(reg).ResolveProjects(selector.Query{Project: &opts.Project})
		if err != nil {
			return project, coremetadata.Window{}, err
		}
		if len(matches.Matches) != 1 {
			return project, coremetadata.Window{}, processCreateUsage("process creation requires exactly one Project")
		}
		found, _ := reg.Project(matches.Matches[0].UID)
		project = *found
		if opts.Window.Name != "" {
			for _, window := range reg.WindowsOf(project.Metadata.UID) {
				if window.Metadata.Name == opts.Window.Name {
					return project, window, nil
				}
			}
		}
		return project, coremetadata.Window{Metadata: coremetadata.ObjectMeta{Name: opts.NewWindow.Name}}, nil
	}
	if opts.Project != (selector.Ref{}) {
		query.Project = &opts.Project
	}
	if opts.Window != (selector.Ref{}) {
		query.Windows = []selector.Ref{opts.Window}
	} else {
		matches, err := selector.New(reg).ResolveProjects(query)
		if err != nil {
			return project, coremetadata.Window{}, err
		}
		if len(matches.Matches) != 1 {
			return project, coremetadata.Window{}, processCreateUsage("process creation requires exactly one Project")
		}
		found, _ := reg.Project(matches.Matches[0].UID)
		project = *found
		ref, err := PrimaryWindowRef(reg, project, "create agent", "--host process")
		if err != nil {
			return project, coremetadata.Window{}, err
		}
		query.Windows = []selector.Ref{ref}
	}
	matches, err := selector.New(reg).ResolveWindows(query)
	if err != nil {
		return project, coremetadata.Window{}, err
	}
	if len(matches.Matches) != 1 {
		return project, coremetadata.Window{}, processCreateUsage("process creation requires exactly one Window")
	}
	window, _ := reg.Window(matches.Matches[0].UID)
	owner, ok := reg.Project(window.Metadata.OwnerUID())
	if !ok {
		return project, coremetadata.Window{}, errors.New("process Window owner is unavailable")
	}
	return *owner, *window, nil
}
