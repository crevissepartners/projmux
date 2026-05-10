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

// DefaultPostCreateTimeout is the maximum wall-clock time the post-create hook
// is allowed to run before it gets killed.
const DefaultPostCreateTimeout = 5 * time.Second

const postCreatePrefix = "[post-create] "

var projectPostCreateCandidates = []string{
	filepath.Join(".projmux", "post-create"),
	filepath.Join(".projmux", "hooks", "post-create"),
}

// PostCreateContext describes the tmux session that was just created and is
// passed to the hook script as PROJMUX_* environment variables.
type PostCreateContext struct {
	SessionName string
	CWD         string
	Kind        string
	Socket      string
	// Version is optional. When empty the runner's Version is used.
	Version string
}

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

// Run executes configured post-create hooks for c. Hook failures
// (missing file, non-executable, non-zero exit, exec error, timeout) are
// recorded as a single warning line on Logger and never returned.
func (r *PostCreateRunner) Run(ctx context.Context, c PostCreateContext) {
	if r == nil {
		return
	}

	paths := r.postCreateHookPaths(c.CWD)
	if len(paths.global) == 0 && paths.project.path == "" {
		return
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultPostCreateTimeout
	}

	for _, path := range paths.global {
		r.runHook(ctx, c, path, timeout)
	}
	if paths.project.path != "" && r.authorizeProjectHook(paths.project) {
		r.runHook(ctx, c, paths.project.path, timeout)
	}
}

type postCreateHookPaths struct {
	global  []string
	project projectPostCreateHook
}

type projectPostCreateHook struct {
	repo string
	rel  string
	path string
}

func (r *PostCreateRunner) postCreateHookPaths(cwd string) postCreateHookPaths {
	var paths postCreateHookPaths
	if path := strings.TrimSpace(r.HookPath); isExecutableHook(path) {
		paths.global = append(paths.global, path)
	}
	if r.DiscoverProjectHooks && !projectHooksDisabled(r.ProjectHooksFilePath, r.Logger) {
		paths.project = discoverProjectPostCreateHook(cwd)
	}
	return paths
}

func discoverProjectPostCreateHook(cwd string) projectPostCreateHook {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return projectPostCreateHook{}
	}
	repo, err := filepath.Abs(cwd)
	if err != nil {
		return projectPostCreateHook{}
	}
	for _, candidate := range projectPostCreateCandidates {
		path := filepath.Join(repo, candidate)
		if isExecutableHook(path) {
			return projectPostCreateHook{
				repo: repo,
				rel:  filepath.ToSlash(candidate),
				path: path,
			}
		}
	}
	return projectPostCreateHook{}
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

func (r *PostCreateRunner) runHook(ctx context.Context, c PostCreateContext, path string, timeout time.Duration) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, path)
	cmd.Stdin = nil
	cmd.Dir = c.CWD
	cmd.Env = buildHookEnv(c, r.Version)
	// Force-close inherited pipes 250ms after SIGKILL so a child that
	// inherited stdout/stderr (e.g. a backgrounded sleep) cannot keep us
	// blocked in cmd.Wait.
	cmd.WaitDelay = 250 * time.Millisecond

	logger := r.Logger
	prefixed := newLinePrefixer(logger, postCreatePrefix)
	cmd.Stdout = prefixed
	cmd.Stderr = prefixed

	err := cmd.Run()
	prefixed.Flush()

	if runCtx.Err() == context.DeadlineExceeded {
		warnf(logger, "hook %q timed out after %s", path, timeout)
		return
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			warnf(logger, "hook %q exited with status %d", path, exitErr.ExitCode())
			return
		}
		warnf(logger, "hook %q: %v", path, err)
	}
}

func buildHookEnv(c PostCreateContext, fallbackVersion string) []string {
	env := append([]string{}, os.Environ()...)
	version := c.Version
	if version == "" {
		version = fallbackVersion
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

func warnf(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "projmux: post-create hook: "+format+"\n", args...)
}
