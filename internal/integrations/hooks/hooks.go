// Package hooks runs optional user-supplied lifecycle hooks at projmux
// lifecycle points (e.g. tmux session creation). Hooks are sourced exclusively
// from declarative [hooks.<event>] run = "..." entries in either the global
// `${XDG_CONFIG_HOME}/projmux/config.toml` or the project-local
// `<repo>/.projmux/config.toml`. Legacy script files in the historical
// `.projmux/<event>` / `.projmux/hooks/<event>` layout are no longer executed;
// MigrateProjectLegacyScripts / MigrateGlobalLegacyScripts handle the
// transition.
package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event names the user-facing lifecycle hook event.
type Event string

const (
	EventPreCreate   Event = "pre-create"
	EventPostCreate  Event = "post-create"
	EventPaneStartup Event = "pane-startup"
	EventPostAttach  Event = "post-attach"
	EventSendNoti    Event = "send-noti"
)

var SupportedEvents = []Event{
	EventPreCreate,
	EventPostCreate,
	EventPaneStartup,
	EventPostAttach,
	EventSendNoti,
}

// DefaultPostCreateTimeout is the maximum wall-clock time a lifecycle hook is
// allowed to run before it gets killed. The name is preserved for the existing
// post-create public API.
const DefaultPostCreateTimeout = 5 * time.Second

// Context describes the tmux lifecycle point and is passed to hook commands as
// PROJMUX_* environment variables.
type Context struct {
	SessionName string
	CWD         string
	Kind        string
	Socket      string
	PaneID      string
	Env         map[string]string
	Stdin       []byte
	// Version is optional. When empty the runner's Version is used.
	Version string
}

// PostCreateContext describes the tmux session that was just created and is
// passed to the post-create hook command as PROJMUX_* environment variables.
type PostCreateContext = Context

// RunResult is the observable hook output returned for events that consume
// stdout, such as pane-startup.
type RunResult struct {
	Stdout string
}

// AsyncResult reports the eventual result of a best-effort background hook.
type AsyncResult struct {
	RunResult RunResult
	Err       error
}

// Runner runs optional global and project-local lifecycle hooks. Missing or
// empty entries degrade to no-ops. Global hooks live in GlobalConfigPath
// (defaults to `${XDG_CONFIG_HOME}/projmux/config.toml`) and are always
// trusted; project-local hooks live in `<cwd>/.projmux/config.toml` and are
// gated by the trust store.
type Runner struct {
	// GlobalConfigPath is the global declarative config file. Empty disables
	// global hooks. A missing file is also treated as empty (no error).
	GlobalConfigPath     string
	DiscoverProjectHooks bool
	ProjectHooksFilePath string
	TrustStorePath       string
	ProjectHookPrompt    ProjectHookPrompt
	PromptReader         io.Reader
	PromptWriter         io.Writer
	Logger               io.Writer
	Timeout              time.Duration
	Version              string
	trustMu              sync.Mutex
	authorized           map[string]string
}

// RunnerByEvent is the event-oriented lifecycle hook runner surface.
type RunnerByEvent = Runner

// PostCreateRunner is a backwards-compatible shim that delegates to Runner
// for the post-create lifecycle point. It is intentionally thin: callers that
// want full event coverage should construct *Runner directly.
type PostCreateRunner struct {
	GlobalConfigPath     string
	DiscoverProjectHooks bool
	ProjectHooksFilePath string
	TrustStorePath       string
	ProjectHookPrompt    ProjectHookPrompt
	PromptReader         io.Reader
	PromptWriter         io.Writer
	Logger               io.Writer
	Timeout              time.Duration
	Version              string
}

// Run executes configured lifecycle hooks for event. Hook failures are logged
// and ignored except for EventPreCreate, where a non-zero exit, exec error, or
// timeout aborts creation by returning an error.
func (r *Runner) Run(ctx context.Context, event Event, c Context) (RunResult, error) {
	if r == nil {
		return RunResult{}, nil
	}
	event = normalizeEvent(event)
	if event == "" {
		return RunResult{}, nil
	}

	globalCfg, hasGlobalCfg := r.globalConfigForEvent(event)
	projectFile := r.discoverProjectConfigFile(event, c.CWD)
	projectCfg, hasProjectCfg := r.projectConfigForEvent(event, projectFile)
	if hasGlobalCfg {
		c.Env = mergeConfigEnv(c.Env, globalCfg.SessionEnv())
	}
	if hasProjectCfg {
		c.Env = mergeConfigEnv(c.Env, projectCfg.SessionEnv())
	}
	if !hasGlobalCfg && !hasProjectCfg {
		return RunResult{}, nil
	}
	if event == EventPaneStartup && ((hasGlobalCfg && globalCfg.hookRun(EventPaneStartup) != "") || (hasProjectCfg && projectCfg.hookRun(EventPaneStartup) != "")) {
		r.warnf(event, "deprecated; move [hooks.pane-startup] run to [startup] run before the next breaking release")
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultPostCreateTimeout
	}

	var result RunResult
	if hasGlobalCfg {
		hookResult, err := r.runConfigHook(ctx, event, c, globalCfg, "global", timeout, hookOutputMode(event))
		if err != nil {
			if event == EventPreCreate {
				return result, err
			}
			r.warnf(event, "global config hook: %v", err)
		} else {
			result = mergeResult(event, result, hookResult)
		}
	}
	if hasProjectCfg {
		hookResult, err := r.runConfigHook(ctx, event, c, projectCfg, "project", timeout, hookOutputMode(event))
		if err != nil {
			if event == EventPreCreate {
				return result, err
			}
			r.warnf(event, "project config %q: %v", projectFile.rel, err)
		} else {
			result = mergeResult(event, result, hookResult)
		}
		if event == EventPaneStartup {
			result = mergeResult(event, result, RunResult{Stdout: projectCfg.StartupRun})
		}
	}
	return result, nil
}

// RunAsync executes configured hooks for event in a background goroutine. The
// returned channel is buffered and closed after completion so tests and
// diagnostic callers can observe the best-effort result without making the
// production dispatch path block on the hook command.
func (r *Runner) RunAsync(ctx context.Context, event Event, c Context) <-chan AsyncResult {
	ch := make(chan AsyncResult, 1)
	if r == nil {
		ch <- AsyncResult{}
		close(ch)
		return ch
	}
	go func() {
		result, err := r.Run(ctx, event, c)
		ch <- AsyncResult{RunResult: result, Err: err}
		close(ch)
	}()
	return ch
}

// ProjectSessionEnv returns trusted project-local config environment that
// should be applied to a newly-created tmux session.
func (r *Runner) ProjectSessionEnv(cwd string) map[string]string {
	if r == nil || !r.DiscoverProjectHooks || projectHooksDisabled(EventPostCreate, r.ProjectHooksFilePath, r.Logger) {
		return nil
	}
	configFile := discoverProjectConfig(cwd)
	if configFile.path == "" {
		return nil
	}
	cfg, err := loadProjectConfig(configFile.path)
	if err != nil {
		r.warnf(EventPostCreate, "project config %q could not be parsed: %v", configFile.rel, err)
		return nil
	}
	if !cfg.hasSessionEnv() || !r.authorizeProjectConfig(EventPostCreate, configFile) {
		return nil
	}
	return cfg.SessionEnv()
}

// HasHooks reports whether a declarative global or project-local hook exists
// for event. It does not perform project trust prompting.
func (r *Runner) HasHooks(event Event, cwd string) bool {
	if r == nil {
		return false
	}
	event = normalizeEvent(event)
	if event == "" {
		return false
	}
	if globalCfg, err := LoadGlobalConfig(r.GlobalConfigPath); err == nil {
		if globalCfg.hasEventSurface(event) {
			return true
		}
	}
	if !r.DiscoverProjectHooks || projectHooksDisabled(event, r.ProjectHooksFilePath, r.Logger) {
		return false
	}
	configFile := discoverProjectConfig(cwd)
	if configFile.path == "" {
		return false
	}
	cfg, err := loadProjectConfig(configFile.path)
	if err != nil {
		r.warnf(event, "project config %q could not be parsed: %v", configFile.rel, err)
		return false
	}
	return cfg.hasEventSurface(event)
}

// Run executes configured post-create hooks for c. Hook failures
// (non-zero exit, exec error, timeout) are recorded as a single warning line
// on Logger and never returned.
func (r *PostCreateRunner) Run(ctx context.Context, c PostCreateContext) {
	if r == nil {
		return
	}
	_, _ = r.runner().Run(ctx, EventPostCreate, c)
}

func (r *PostCreateRunner) runner() *Runner {
	return &Runner{
		GlobalConfigPath:     r.GlobalConfigPath,
		DiscoverProjectHooks: r.DiscoverProjectHooks,
		ProjectHooksFilePath: r.ProjectHooksFilePath,
		TrustStorePath:       r.TrustStorePath,
		ProjectHookPrompt:    r.ProjectHookPrompt,
		PromptReader:         r.PromptReader,
		PromptWriter:         r.PromptWriter,
		Logger:               r.Logger,
		Timeout:              r.Timeout,
		Version:              r.Version,
	}
}

type outputMode int

const (
	outputLog outputMode = iota
	outputCaptureStdout
)

func hookOutputMode(event Event) outputMode {
	if event == EventPaneStartup {
		return outputCaptureStdout
	}
	return outputLog
}

func mergeResult(event Event, current, next RunResult) RunResult {
	if event == EventPaneStartup {
		if trimmed := strings.TrimSpace(next.Stdout); trimmed != "" {
			current.Stdout = trimmed
		}
		return current
	}
	return current
}

func (r *Runner) globalConfigForEvent(event Event) (ProjectConfig, bool) {
	cfg, err := LoadGlobalConfig(r.GlobalConfigPath)
	if err != nil {
		r.warnf(event, "global config %q could not be parsed: %v", r.GlobalConfigPath, err)
		return ProjectConfig{}, false
	}
	if !cfg.hasEventSurface(event) {
		return ProjectConfig{}, false
	}
	return cfg, true
}

func (r *Runner) discoverProjectConfigFile(event Event, cwd string) projectConfigFile {
	if !r.DiscoverProjectHooks || projectHooksDisabled(event, r.ProjectHooksFilePath, r.Logger) {
		return projectConfigFile{}
	}
	return discoverProjectConfig(cwd)
}

func (r *Runner) projectConfigForEvent(event Event, configFile projectConfigFile) (ProjectConfig, bool) {
	if configFile.path == "" {
		return ProjectConfig{}, false
	}
	cfg, err := loadProjectConfig(configFile.path)
	if err != nil {
		r.warnf(event, "project config %q could not be parsed: %v", configFile.rel, err)
		return ProjectConfig{}, false
	}
	if !cfg.relevantForEvent(event) {
		return ProjectConfig{}, false
	}
	if !r.authorizeProjectConfig(event, configFile) {
		return ProjectConfig{}, false
	}
	return cfg, true
}

func (r *Runner) runConfigHook(ctx context.Context, event Event, c Context, cfg ProjectConfig, label string, timeout time.Duration, mode outputMode) (RunResult, error) {
	command := cfg.hookRun(event)
	if strings.TrimSpace(command) == "" {
		return RunResult{}, nil
	}
	return r.runCommand(ctx, event, c, "sh", []string{"-c", command}, label, timeout, mode)
}

func (r *Runner) runCommand(ctx context.Context, event Event, c Context, name string, args []string, label string, timeout time.Duration, mode outputMode) (RunResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.Dir = c.CWD

	cmd.Env = buildHookEnv(c, r.Version)
	// Force-close inherited pipes 250ms after SIGKILL so a child that
	// inherited stdout/stderr (e.g. a backgrounded sleep) cannot keep us
	// blocked in cmd.Wait.
	cmd.WaitDelay = 250 * time.Millisecond

	logger := r.Logger
	// Note: label (e.g. "global"/"project") is intentionally NOT embedded in
	// the line prefix so existing log scrapers and tests that anchor on the
	// "[event]" shape keep working. Pass label as a contextual hint via the
	// warning text instead.
	_ = label
	prefix := "[" + string(event) + "] "
	prefixed := newLinePrefixer(logger, prefix)
	var stdout bytes.Buffer
	if mode == outputCaptureStdout {
		cmd.Stdout = &stdout
	} else {
		cmd.Stdout = prefixed
	}
	cmd.Stderr = prefixed

	err := cmd.Run()
	prefixed.Flush()

	if runCtx.Err() == context.DeadlineExceeded {
		return RunResult{}, fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return RunResult{}, fmt.Errorf("exited with status %d", exitErr.ExitCode())
		}
		return RunResult{}, err
	}
	return RunResult{Stdout: strings.TrimSpace(stdout.String())}, nil
}

func normalizeEvent(event Event) Event {
	switch event {
	case EventPreCreate, EventPostCreate, EventPaneStartup, EventPostAttach, EventSendNoti:
		return event
	default:
		return ""
	}
}

// IsDeprecatedEvent reports events that still run for compatibility but are
// on the documented removal path.
func IsDeprecatedEvent(event Event) bool {
	return normalizeEvent(event) == EventPaneStartup
}

// DisplayEventName returns the human-facing event label used by CLI and
// Settings surfaces.
func DisplayEventName(event Event) string {
	name := string(event)
	if IsDeprecatedEvent(event) {
		return name + " (deprecated)"
	}
	return name
}

func buildHookEnv(c Context, fallbackVersion string) []string {
	env := append([]string{}, os.Environ()...)
	version := c.Version
	if version == "" {
		version = fallbackVersion
	}
	for _, key := range sortedEnvKeys(c.Env) {
		env = append(env, key+"="+c.Env[key])
	}
	env = append(env,
		"PROJMUX_SESSION="+c.SessionName,
		"PROJMUX_CWD="+c.CWD,
		"PROJMUX_SESSION_KIND="+c.Kind,
		"PROJMUX_VERSION="+version,
	)
	if strings.TrimSpace(c.Socket) != "" {
		env = append(env, "PROJMUX_SOCKET="+c.Socket)
	}
	if strings.TrimSpace(c.PaneID) != "" {
		env = append(env, "PROJMUX_PANE="+c.PaneID)
	}
	return env
}

// linePrefixer wraps a destination writer and rewrites bytes into newline
// terminated lines prefixed with prefix. It is safe for concurrent writes from
// child stdout and stderr because exec.Cmd serializes through its lock when
// the same writer is assigned to both — but we still guard with a mutex so
// future callers can share a logger without surprises.
type linePrefixer struct {
	mu     sync.Mutex
	dst    io.Writer
	prefix string
	buf    bytes.Buffer
}

func newLinePrefixer(dst io.Writer, prefix string) *linePrefixer {
	return &linePrefixer{dst: dst, prefix: prefix}
}

func (p *linePrefixer) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dst == nil {
		return len(b), nil
	}
	n := len(b)
	for len(b) > 0 {
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			p.buf.Write(b)
			break
		}
		p.buf.Write(b[:idx])
		_, _ = io.WriteString(p.dst, p.prefix+p.buf.String()+"\n")
		p.buf.Reset()
		b = b[idx+1:]
	}
	return n, nil
}

// Flush writes any buffered partial line (no trailing newline) to dst.
func (p *linePrefixer) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dst == nil || p.buf.Len() == 0 {
		return
	}
	_, _ = io.WriteString(p.dst, p.prefix+p.buf.String()+"\n")
	p.buf.Reset()
}

func (r *Runner) warnf(event Event, format string, args ...any) {
	warnf(r.Logger, event, format, args...)
}

func warnf(w io.Writer, event Event, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "projmux: %s hook: "+format+"\n", append([]any{event}, args...)...)
}
