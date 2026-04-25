package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultKubeCacheTTL     = 5 * time.Second
	defaultKubeCommandLimit = 400 * time.Millisecond
)

type statusCommand struct {
	lookupEnv   func(string) string
	homeDir     func() (string, error)
	readCommand func(ctx context.Context, name string, args ...string) ([]byte, error)
	now         func() time.Time
}

func newStatusCommand() *statusCommand {
	return &statusCommand{
		lookupEnv:   os.Getenv,
		homeDir:     os.UserHomeDir,
		readCommand: readExternalCommand,
		now:         time.Now,
	}
}

func (c *statusCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printStatusUsage(stderr)
		return errors.New("status requires a subcommand")
	}

	switch args[0] {
	case "git":
		return c.runGit(args[1:], stdout, stderr)
	case "kube":
		return c.runKube(args[1:], stdout, stderr)
	case "help", "--help", "-h":
		printStatusUsage(stdout)
		return nil
	default:
		printStatusUsage(stderr)
		return fmt.Errorf("unknown status subcommand: %s", args[0])
	}
}

func (c *statusCommand) runGit(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 {
		printStatusUsage(stderr)
		return errors.New("status git accepts at most 1 [path] argument")
	}
	path := ""
	if len(args) == 1 {
		path = strings.TrimSpace(args[0])
	} else if c.env("TMUX") != "" {
		path = c.readTrimmed("tmux", "display-message", "-p", "#{pane_current_path}")
	}
	if path == "" {
		return nil
	}
	if _, err := c.read("git", "-C", path, "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil
	}
	branch := c.readTrimmed("git", "-C", path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch == "" {
		branch = c.readTrimmed("git", "-C", path, "rev-parse", "--short", "HEAD")
	}
	if branch == "" {
		return nil
	}
	_, err := fmt.Fprintf(stdout, " | git:%s", branch)
	return err
}

func (c *statusCommand) runKube(args []string, stdout, stderr io.Writer) error {
	if len(args) > 1 {
		printStatusUsage(stderr)
		return errors.New("status kube accepts at most 1 [session] argument")
	}
	sessionName := ""
	if len(args) == 1 {
		sessionName = strings.TrimSpace(args[0])
	} else {
		sessionName = c.readTrimmed("tmux", "display-message", "-p", "#S")
	}
	if sessionName == "" {
		return nil
	}
	segment := c.kubeSegment(sessionName)
	if segment == "" {
		return nil
	}
	_, err := fmt.Fprint(stdout, segment)
	return err
}

func (c *statusCommand) kubeSegment(sessionName string) string {
	if c.readTrimmed("command", "-v", "kubectl") == "" {
		return ""
	}
	cacheFile := c.kubeCacheFile(sessionName)
	cached := readTextFile(cacheFile)
	if info, err := os.Stat(cacheFile); err == nil && c.now().Sub(info.ModTime()) < c.kubeCacheTTL() {
		return cached
	}

	kubeConfig := c.kubeSessionPath(sessionName)
	if kubeConfig != "" {
		if _, err := os.Stat(kubeConfig); err != nil {
			kubeConfig = ""
		}
	}

	ctx := c.kubectlTrimmed(kubeConfig, "config", "current-context")
	if ctx == "" {
		return cached
	}
	ns := c.kubectlTrimmed(kubeConfig, "config", "view", "--minify", "--output", "jsonpath={..namespace}")
	if ns == "" {
		ns = "default"
	}
	segment := fmt.Sprintf("k8s:#[fg=red]%s#[default]/#[fg=blue]%s#[default]", ctx, ns)
	_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
	_ = os.WriteFile(cacheFile, []byte(segment), 0o644)
	return segment
}

func (c *statusCommand) kubectlTrimmed(kubeConfig string, args ...string) string {
	timeoutValue := formatStatusTimeout(c.kubeCommandLimit())
	if c.readTrimmed("command", "-v", "timeout") != "" {
		command := []string{"timeout", timeoutValue, "kubectl"}
		command = append(command, args...)
		if kubeConfig != "" {
			command = append([]string{"KUBECONFIG=" + kubeConfig}, command...)
			return c.readTrimmed("env", command...)
		}
		return c.readTrimmed(command[0], command[1:]...)
	}
	if kubeConfig != "" {
		command := append([]string{"KUBECONFIG=" + kubeConfig, "kubectl"}, args...)
		return c.readTrimmed("env", command...)
	}
	return c.readTrimmed("kubectl", args...)
}

func (c *statusCommand) kubeSessionPath(sessionName string) string {
	if strings.TrimSpace(sessionName) == "" {
		return ""
	}
	return filepath.Join(c.kubeSessionBaseDir(), sessionName+".yaml")
}

func (c *statusCommand) kubeSessionBaseDir() string {
	root := strings.TrimRight(c.env("XDG_RUNTIME_DIR"), string(os.PathSeparator))
	if root == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			root = "."
		} else {
			root = filepath.Join(homeDir, ".cache")
		}
	}
	return filepath.Join(root, "kube-sessions")
}

func (c *statusCommand) kubeCacheFile(sessionName string) string {
	slug := strings.ReplaceAll(sessionName, "/", "-")
	slug = strings.ReplaceAll(slug, ".", "_")
	return filepath.Join(c.kubeCacheDir(), "kube-segment-"+slug+".txt")
}

func (c *statusCommand) kubeCacheDir() string {
	cacheHome := strings.TrimRight(c.env("XDG_CACHE_HOME"), string(os.PathSeparator))
	if cacheHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			cacheHome = ".cache"
		} else {
			cacheHome = filepath.Join(homeDir, ".cache")
		}
	}
	return filepath.Join(cacheHome, "tmux")
}

func (c *statusCommand) kubeCacheTTL() time.Duration {
	seconds := parsePositiveInt(c.env("TMUX_KUBE_CACHE_TTL"))
	if seconds <= 0 {
		return defaultKubeCacheTTL
	}
	return time.Duration(seconds) * time.Second
}

func (c *statusCommand) kubeCommandLimit() time.Duration {
	value := strings.TrimSpace(c.env("TMUX_KUBE_TIMEOUT"))
	if value == "" {
		return defaultKubeCommandLimit
	}
	if strings.ContainsAny(value, "hmsuµns") {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			return d
		}
	}
	parts := strings.SplitN(value, ".", 2)
	seconds := parsePositiveInt(parts[0])
	millis := 0
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		millis = parsePositiveInt(frac)
	}
	d := time.Duration(seconds)*time.Second + time.Duration(millis)*time.Millisecond
	if d <= 0 {
		return defaultKubeCommandLimit
	}
	return d
}

func (c *statusCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("status home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *statusCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *statusCommand) read(name string, args ...string) ([]byte, error) {
	if c.readCommand == nil {
		return nil, errors.New("status command reader is not configured")
	}
	return c.readCommand(context.Background(), name, args...)
}

func (c *statusCommand) readTrimmed(name string, args ...string) string {
	out, err := c.read(name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func formatStatusTimeout(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d", int(d/time.Second))
	}
	return fmt.Sprintf("%.3f", d.Seconds())
}

func readTextFile(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func printStatusUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux status git [path]")
	fmt.Fprintln(w, "  projmux status kube [session]")
}
