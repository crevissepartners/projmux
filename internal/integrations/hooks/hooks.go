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
	"strings"
	"sync"
	"time"
)

// DefaultPostCreateTimeout is the maximum wall-clock time the post-create hook
// is allowed to run before it gets killed.
const DefaultPostCreateTimeout = 5 * time.Second

const postCreatePrefix = "[post-create] "

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

// PostCreateRunner runs the optional post-create hook script at HookPath.
// A nil receiver, an empty HookPath, or a missing/non-executable file all
// degrade to a silent no-op so the caller can always invoke Run unconditionally.
type PostCreateRunner struct {
	HookPath string
	Logger   io.Writer
	Timeout  time.Duration
	Version  string
}

// Run executes the post-create hook for c if one is configured. Hook failures
// (missing file, non-executable, non-zero exit, exec error, timeout) are
// recorded as a single warning line on Logger and never returned.
func (r *PostCreateRunner) Run(ctx context.Context, c PostCreateContext) {
	if r == nil || strings.TrimSpace(r.HookPath) == "" {
		return
	}

	info, err := os.Stat(r.HookPath)
	if err != nil {
		return
	}
	if info.IsDir() {
		return
	}
	if info.Mode().Perm()&0o100 == 0 {
		return
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultPostCreateTimeout
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.HookPath)
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

	err = cmd.Run()
	prefixed.Flush()

	if runCtx.Err() == context.DeadlineExceeded {
		warnf(logger, "hook %q timed out after %s", r.HookPath, timeout)
		return
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			warnf(logger, "hook %q exited with status %d", r.HookPath, exitErr.ExitCode())
			return
		}
		warnf(logger, "hook %q: %v", r.HookPath, err)
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

func warnf(w io.Writer, format string, args ...interface{}) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "projmux: post-create hook: "+format+"\n", args...)
}
