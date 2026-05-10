// Package hooks runs optional user-supplied hook scripts at projmux
// lifecycle points (e.g. tmux session creation).
package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
)

var SupportedEvents = []Event{
	EventPreCreate,
	EventPostCreate,
	EventPaneStartup,
	EventPostAttach,
}

// DefaultPostCreateTimeout is the maximum wall-clock time a lifecycle hook is
// allowed to run before it gets killed. The name is preserved for the existing
// post-create public API.
const DefaultPostCreateTimeout = 5 * time.Second

// Context describes the tmux lifecycle point and is passed to hook scripts as
// PROJMUX_* environment variables.
type Context struct {
	SessionName string
	CWD         string
	Kind        string
	Socket      string
	PaneID      string
	Env         map[string]string
	// Version is optional. When empty the runner's Version is used.
	Version string
}

// PostCreateContext describes the tmux session that was just created and is
// passed to the post-create hook script as PROJMUX_* environment variables.
type PostCreateContext = Context

// RunResult is the observable hook output returned for events that consume
// stdout, such as pane-startup.
type RunResult struct {
	Stdout string
}

// Runner runs optional global and project-local lifecycle hooks. Missing,
// non-executable, or empty paths degrade to no-ops.
type Runner struct {
	GlobalHookPaths      map[Event][]string
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

// PostCreateRunner runs the optional global post-create hook at HookPath, then
// an optional project-local post-create hook when DiscoverProjectHooks is set.
// A nil receiver, empty paths, and missing/non-executable files all degrade to
// silent no-ops so the caller can always invoke Run unconditionally.
type PostCreateRunner struct {
	HookPath             string
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

	paths := r.hookPaths(event, c.CWD)
	hasFileHooks := len(paths.global) > 0 || paths.project.path != ""
	projectConfig, hasProjectConfig := r.projectConfigForEvent(event, paths.config, hasFileHooks)
	if hasProjectConfig {
		c.Env = mergeConfigEnv(c.Env, projectConfig.SessionEnv())
	}
	if len(paths.global) == 0 && paths.project.path == "" && !hasProjectConfig {
		return RunResult{}, nil
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultPostCreateTimeout
	}

	var result RunResult
	for _, path := range paths.global {
		hookResult, err := r.runHook(ctx, event, c, path, timeout, hookOutputMode(event))
		if err != nil {
			if event == EventPreCreate {
				return result, err
			}
			r.warnf(event, "hook %q: %v", path, err)
			continue
		}
		result = mergeResult(event, result, hookResult)
	}
	if paths.project.path != "" && r.authorizeProjectHook(paths.project) {
		hookResult, err := r.runHook(ctx, event, c, paths.project.path, timeout, hookOutputMode(event))
		if err != nil {
			if event == EventPreCreate {
				return result, err
			}
			r.warnf(event, "hook %q: %v", paths.project.path, err)
			return result, nil
		}
		result = mergeResult(event, result, hookResult)
	}
	if hasProjectConfig {
		hookResult, err := r.runProjectConfigHook(ctx, event, c, projectConfig, timeout, hookOutputMode(event))
		if err != nil {
			if event == EventPreCreate {
				return result, err
			}
			r.warnf(event, "config %q: %v", paths.config.rel, err)
			return result, nil
		}
		result = mergeResult(event, result, hookResult)
		if event == EventPaneStartup {
			result = mergeResult(event, result, RunResult{Stdout: projectConfig.StartupRun})
		}
	}
	return result, nil
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

// HasHooks reports whether an executable global or project-local hook exists
// for event. It does not perform project trust prompting.
func (r *Runner) HasHooks(event Event, cwd string) bool {
	if r == nil {
		return false
	}
	event = normalizeEvent(event)
	if event == "" {
		return false
	}
	paths := r.hookPaths(event, cwd)
	if len(paths.global) > 0 || paths.project.path != "" {
		return true
	}
	if paths.config.path == "" {
		return false
	}
	cfg, err := loadProjectConfig(paths.config.path)
	if err != nil {
		r.warnf(event, "project config %q could not be parsed: %v", paths.config.rel, err)
		return false
	}
	return cfg.hasEventSurface(event)
}

// Run executes configured post-create hooks for c. Hook failures
// (missing file, non-executable, non-zero exit, exec error, timeout) are
// recorded as a single warning line on Logger and never returned.
func (r *PostCreateRunner) Run(ctx context.Context, c PostCreateContext) {
	if r == nil {
		return
	}
	_, _ = r.runner().Run(ctx, EventPostCreate, c)
}

type hookPaths struct {
	global  []string
	project projectHook
	config  projectConfigFile
}

type projectHook struct {
	event Event
	repo  string
	rel   string
	path  string
}

func (r *PostCreateRunner) runner() *Runner {
	return &Runner{
		GlobalHookPaths: map[Event][]string{
			EventPostCreate: {r.HookPath},
		},
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

func (r *Runner) hookPaths(event Event, cwd string) hookPaths {
	var paths hookPaths
	for _, path := range r.GlobalHookPaths[event] {
		if path = strings.TrimSpace(path); isExecutableHook(path) {
			paths.global = append(paths.global, path)
		}
	}
	if r.DiscoverProjectHooks && !projectHooksDisabled(event, r.ProjectHooksFilePath, r.Logger) {
		paths.project = discoverProjectHook(event, cwd)
		paths.config = discoverProjectConfig(cwd)
	}
	return paths
}

func discoverProjectHook(event Event, cwd string) projectHook {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return projectHook{}
	}
	repo, err := filepath.Abs(cwd)
	if err != nil {
		return projectHook{}
	}
	for _, candidate := range projectHookCandidates(event) {
		path := filepath.Join(repo, candidate)
		if isExecutableHook(path) {
			return projectHook{
				event: event,
				repo:  repo,
				rel:   filepath.ToSlash(candidate),
				path:  path,
			}
		}
	}
	return projectHook{}
}

func projectHookCandidates(event Event) []string {
	name := string(normalizeEvent(event))
	if name == "" {
		return nil
	}
	return []string{
		filepath.Join(".projmux", name),
		filepath.Join(".projmux", "hooks", name),
	}
}

func isExecutableHook(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o100 != 0
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

func (r *Runner) projectConfigForEvent(event Event, configFile projectConfigFile, hasHooks bool) (ProjectConfig, bool) {
	if configFile.path == "" {
		return ProjectConfig{}, false
	}
	cfg, err := loadProjectConfig(configFile.path)
	if err != nil {
		r.warnf(event, "project config %q could not be parsed: %v", configFile.rel, err)
		return ProjectConfig{}, false
	}
	if !cfg.relevantForEvent(event, hasHooks) {
		return ProjectConfig{}, false
	}
	if !r.authorizeProjectConfig(event, configFile) {
		return ProjectConfig{}, false
	}
	return cfg, true
}

func (r *Runner) runHook(ctx context.Context, event Event, c Context, path string, timeout time.Duration, mode outputMode) (RunResult, error) {
	return r.runCommand(ctx, event, c, path, nil, timeout, mode)
}

func (r *Runner) runProjectConfigHook(ctx context.Context, event Event, c Context, cfg ProjectConfig, timeout time.Duration, mode outputMode) (RunResult, error) {
	command := cfg.hookRun(event)
	if strings.TrimSpace(command) == "" {
		return RunResult{}, nil
	}
	return r.runCommand(ctx, event, c, "sh", []string{"-c", command}, timeout, mode)
}

func (r *Runner) runCommand(ctx context.Context, event Event, c Context, name string, args []string, timeout time.Duration, mode outputMode) (RunResult, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Stdin = nil
	cmd.Dir = c.CWD

	cmd.Env = buildHookEnv(c, r.Version)
	// Force-close inherited pipes 250ms after SIGKILL so a child that
	// inherited stdout/stderr (e.g. a backgrounded sleep) cannot keep us
	// blocked in cmd.Wait.
	cmd.WaitDelay = 250 * time.Millisecond

	logger := r.Logger
	prefixed := newLinePrefixer(logger, "["+string(event)+"] ")
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
	case EventPreCreate, EventPostCreate, EventPaneStartup, EventPostAttach:
		return event
	default:
		return ""
	}
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
