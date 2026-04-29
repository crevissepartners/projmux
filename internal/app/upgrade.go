package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// upgradeCommand wraps `go install` to refresh the active projmux binary.
type upgradeCommand struct {
	executable    func() (string, error)
	evalSymlinks  func(string) (string, error)
	lookPath      func(string) (string, error)
	mkdirTemp     func(dir, pattern string) (string, error)
	rename        func(oldpath, newpath string) error
	chmod         func(name string, mode os.FileMode) error
	stat          func(name string) (os.FileInfo, error)
	removeAll     func(path string) error
	remove        func(name string) error
	environ       func() []string
	runCmd        func(cmd *exec.Cmd) error
	newCmd        func(name string, args ...string) *exec.Cmd
	copyFile      func(src, dst string) error
	tempSuffixGen func() string
}

func newUpgradeCommand() *upgradeCommand {
	return &upgradeCommand{
		executable:    os.Executable,
		evalSymlinks:  filepath.EvalSymlinks,
		lookPath:      exec.LookPath,
		mkdirTemp:     os.MkdirTemp,
		rename:        os.Rename,
		chmod:         os.Chmod,
		stat:          os.Stat,
		removeAll:     os.RemoveAll,
		remove:        os.Remove,
		environ:       os.Environ,
		runCmd:        func(cmd *exec.Cmd) error { return cmd.Run() },
		newCmd:        exec.Command,
		copyFile:      copyRegularFile,
		tempSuffixGen: defaultTempSuffix,
	}
}

type upgradeOptions struct {
	ref     string
	target  string
	module  string
	dryRun  bool
	noApply bool
}

const (
	defaultUpgradeRef    = "latest"
	defaultUpgradeModule = "github.com/crevissepartners/projmux/cmd/projmux"
)

// Run executes the projmux upgrade self-update flow.
func (c *upgradeCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ref := fs.String("ref", defaultUpgradeRef, "module version reference (e.g. latest, v0.2.0, main)")
	target := fs.String("target", "", "target binary path to replace (default: current executable)")
	module := fs.String("module", defaultUpgradeModule, "go module path to install")
	dryRun := fs.Bool("dry-run", false, "print the commands that would run without executing them")
	noApply := fs.Bool("no-apply", false, "skip running 'projmux tmux apply' after the upgrade")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("upgrade does not accept positional arguments")
	}

	opts := upgradeOptions{
		ref:     strings.TrimSpace(*ref),
		target:  strings.TrimSpace(*target),
		module:  strings.TrimSpace(*module),
		dryRun:  *dryRun,
		noApply: *noApply,
	}

	normalizedRef, err := normalizeUpgradeRef(opts.ref)
	if err != nil {
		return err
	}
	opts.ref = normalizedRef

	if opts.module == "" {
		return errors.New("upgrade requires a non-empty --module path")
	}

	resolvedTarget, err := c.resolveTarget(opts.target)
	if err != nil {
		return err
	}
	opts.target = resolvedTarget

	if opts.dryRun {
		return c.runDryRun(opts, stdout)
	}

	return c.runUpgrade(opts, stdout, stderr)
}

func (c *upgradeCommand) runDryRun(opts upgradeOptions, stdout io.Writer) error {
	tmpDir := filepath.Join(filepath.Dir(opts.target), ".projmux-upgrade-XXXXXX")
	if _, err := fmt.Fprintf(stdout, "would run: GOBIN=%s go install %s@%s\n", tmpDir, opts.module, opts.ref); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "would replace: %s (atomic via temp file)\n", opts.target); err != nil {
		return err
	}
	if !opts.noApply {
		if _, err := fmt.Fprintf(stdout, "would run: %s tmux apply\n", opts.target); err != nil {
			return err
		}
	}
	return nil
}

func (c *upgradeCommand) runUpgrade(opts upgradeOptions, stdout, stderr io.Writer) error {
	if home, err := os.UserHomeDir(); err == nil {
		if raw, _ := preferredProjdirEnv(os.Getenv); raw != "" {
			// PROJMUX_PROJDIR is a PATH-style multi-value; only the
			// primary (first non-empty) entry is persisted to the
			// saved projdir file so the saved value remains a
			// single canonical path.
			if primary := firstProjdirPath(raw); primary != "" {
				memoizeProjdir(home, primary, config.LoadProjdir, config.SaveProjdir)
			}
		}
	}
	if c.lookPath == nil {
		return errors.New("configure upgrade lookPath: lookup function is not configured")
	}
	goBin, err := c.lookPath("go")
	if err != nil {
		return errors.New("upgrade requires the 'go' toolchain in PATH")
	}

	if c.mkdirTemp == nil {
		return errors.New("configure upgrade mkdirTemp: temp directory factory is not configured")
	}
	tmpDir, err := c.mkdirTemp(filepath.Dir(opts.target), ".projmux-upgrade-*")
	if err != nil {
		return fmt.Errorf("create upgrade scratch directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup && c.removeAll != nil {
			_ = c.removeAll(tmpDir)
		}
	}()

	if _, err := fmt.Fprintf(stdout, ">> fetching %s@%s via go install\n", opts.module, opts.ref); err != nil {
		return err
	}

	installArg := opts.module + "@" + opts.ref
	cmd := c.newCmd(goBin, "install", installArg)
	env := append([]string{}, c.environ()...)
	env = filterEnvKeys(env, "GOBIN")
	env = append(env, "GOBIN="+tmpDir)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := c.runCmd(cmd); err != nil {
		return fmt.Errorf("go install %s: %w", installArg, err)
	}

	installed := filepath.Join(tmpDir, "projmux")
	info, err := c.stat(installed)
	if err != nil {
		return fmt.Errorf("locate freshly installed projmux at %s: %w", installed, err)
	}
	if info.IsDir() {
		return fmt.Errorf("expected file at %s, found directory", installed)
	}

	if _, err := fmt.Fprintf(stdout, ">> installed: %s\n", installed); err != nil {
		return err
	}

	if err := c.atomicReplace(installed, opts.target); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, ">> atomically replaced %s\n", opts.target); err != nil {
		return err
	}

	if c.removeAll != nil {
		_ = c.removeAll(tmpDir)
	}
	cleanup = false

	if opts.noApply {
		return nil
	}

	if _, err := fmt.Fprintln(stdout, ">> applying live config..."); err != nil {
		return err
	}
	applyCmd := c.newCmd(opts.target, "tmux", "apply")
	applyCmd.Stdout = stdout
	applyCmd.Stderr = stderr
	if err := c.runCmd(applyCmd); err != nil {
		return fmt.Errorf("apply live config via %s tmux apply: %w", opts.target, err)
	}
	return nil
}

func (c *upgradeCommand) atomicReplace(src, target string) error {
	suffix := "tmp"
	if c.tempSuffixGen != nil {
		suffix = c.tempSuffixGen()
	}
	tmpfile := target + ".upgrade." + suffix
	if c.rename == nil {
		return errors.New("configure upgrade rename: rename function is not configured")
	}
	if err := c.rename(src, tmpfile); err != nil {
		// Cross-device or other rename failure: fall back to copy + remove.
		if c.copyFile == nil {
			return fmt.Errorf("rename installed binary into target directory: %w", err)
		}
		if copyErr := c.copyFile(src, tmpfile); copyErr != nil {
			return fmt.Errorf("rename and copy installed binary into target directory failed: rename=%v copy=%w", err, copyErr)
		}
		if c.remove != nil {
			_ = c.remove(src)
		}
	}
	if c.chmod != nil {
		if err := c.chmod(tmpfile, 0o755); err != nil {
			if c.remove != nil {
				_ = c.remove(tmpfile)
			}
			return fmt.Errorf("chmod upgrade staging file: %w", err)
		}
	}
	if err := c.rename(tmpfile, target); err != nil {
		if c.remove != nil {
			_ = c.remove(tmpfile)
		}
		if isPermissionError(err) {
			return fmt.Errorf("permission denied: %s (try sudo or use --target)", target)
		}
		return fmt.Errorf("atomically replace %s: %w", target, err)
	}
	return nil
}

func (c *upgradeCommand) resolveTarget(override string) (string, error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve upgrade target path: %w", err)
		}
		if c.evalSymlinks != nil {
			if real, err := c.evalSymlinks(abs); err == nil {
				return real, nil
			}
		}
		return abs, nil
	}
	if c.executable == nil {
		return "", errors.New("configure upgrade executable: executable resolver is not configured")
	}
	exe, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable path: %w", err)
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("resolve current executable absolute path: %w", err)
	}
	if c.evalSymlinks != nil {
		if real, err := c.evalSymlinks(abs); err == nil {
			return real, nil
		}
	}
	return abs, nil
}

func normalizeUpgradeRef(ref string) (string, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return "", errors.New("--ref must not be empty")
	}
	trimmed = strings.TrimPrefix(trimmed, "@")
	if trimmed == "" {
		return "", errors.New("--ref must not be empty after stripping leading '@'")
	}
	if strings.ContainsAny(trimmed, " \t\n\r") {
		return "", fmt.Errorf("--ref must not contain whitespace: %q", ref)
	}
	if strings.Contains(trimmed, "@") {
		return "", fmt.Errorf("--ref must not contain '@': %q", ref)
	}
	return trimmed, nil
}

func filterEnvKeys(env []string, keys ...string) []string {
	if len(keys) == 0 {
		return env
	}
	out := env[:0:0]
	for _, entry := range env {
		drop := false
		for _, key := range keys {
			if strings.HasPrefix(entry, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, os.ErrPermission)
}

func copyRegularFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source binary for copy: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open destination for copy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy binary content: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination after copy: %w", err)
	}
	return nil
}

func defaultTempSuffix() string {
	return fmt.Sprintf("%d", os.Getpid())
}
