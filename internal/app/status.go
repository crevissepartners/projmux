package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
)

const (
	defaultKubeCacheTTL     = 5 * time.Second
	defaultKubeCommandLimit = 400 * time.Millisecond
)

type statusCommand struct {
	lookupEnv     func(string) string
	homeDir       func() (string, error)
	readCommand   func(ctx context.Context, name string, args ...string) ([]byte, error)
	now           func() time.Time
	usage         *usageCommand
	notifyStoreFn func() (notifyStore, error)
}

func newStatusCommand() *statusCommand {
	return &statusCommand{
		lookupEnv:     os.Getenv,
		homeDir:       os.UserHomeDir,
		readCommand:   readExternalCommand,
		now:           time.Now,
		usage:         newUsageCommand(),
		notifyStoreFn: defaultStatusNotifyStore,
	}
}

// defaultStatusNotifyStore resolves the canonical notify queue used by the
// status bar segment. Failures here become silent emptiness — the status
// segment must never fail loudly during the tmux refresh interval.
func defaultStatusNotifyStore() (notifyStore, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return nil, fmt.Errorf("resolve default config paths: %w", err)
	}
	return notify.NewDefaultStore(paths), nil
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
	case "usage":
		if c.usage == nil {
			c.usage = newUsageCommand()
		}
		return c.usage.runStatus(args[1:], stdout, stderr)
	case "notify":
		return c.runNotify(args[1:], stdout, stderr)
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
	_, err := fmt.Fprintf(stdout, " #[bold,fg=colour16,bg=colour45] %s #[default]", branch)
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
	fmt.Fprintln(w, "  projmux status usage [--max-width N]")
	fmt.Fprintln(w, "  projmux status notify [--max-width N]")
}

// defaultStatusNotifyMaxWidth bounds the rendered notification segment so it
// cannot blow out the status line on narrow terminals.
const defaultStatusNotifyMaxWidth = 200

// runNotify renders the newest entry in the notify queue as a tmux status
// segment. The segment is intentionally silent on failure — the tmux status
// interval polls this command and must never produce a stack trace.
func (c *statusCommand) runNotify(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("status notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	maxWidth := fs.Int("max-width", defaultStatusNotifyMaxWidth, "truncate the inner text to N runes (0 = no truncation)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("status notify does not accept positional arguments")
	}

	store, err := c.notifyStore()
	if err != nil {
		// Status segments must never fail loudly.
		return nil
	}
	entries, err := store.List()
	if err != nil {
		return nil
	}
	out := formatStatusNotify(entries, *maxWidth)
	if out == "" {
		return nil
	}
	_, err = fmt.Fprint(stdout, out)
	return err
}

func (c *statusCommand) notifyStore() (notifyStore, error) {
	if c.notifyStoreFn == nil {
		return nil, errors.New("status notify store factory is not configured")
	}
	return c.notifyStoreFn()
}

// formatStatusNotify renders the newest entry of the queue as a single tmux
// status segment. The output is plain ASCII (no emoji) and ends with a tmux
// `#[default]` reset so adjacent segments are not stained by colors.
//
// Layout: `[X] <text> · <session> +<N>` where X is one of I/W/!.
func formatStatusNotify(entries []notify.Notification, maxWidth int) string {
	if len(entries) == 0 {
		return ""
	}
	head := entries[0]
	extras := len(entries) - 1
	prefix := "[" + severityLetter(head.Severity) + "] "
	suffix := " " + middotSeparator() + " " + head.Session
	plus := ""
	if extras > 0 {
		plus = fmt.Sprintf(" +%d", extras)
	}

	overhead := runeLen(prefix) + runeLen(suffix) + runeLen(plus)
	text := strings.TrimSpace(head.Text)
	if maxWidth > 0 {
		room := maxWidth - overhead
		if room < 1 {
			room = 1
		}
		if runeLen(text) > room {
			rs := []rune(text)
			if room <= 1 {
				text = string(rs[:room])
			} else {
				text = string(rs[:room-1]) + "."
			}
		}
	}

	color := severityColor(head.Severity)
	return color + prefix + text + suffix + plus + "#[default]"
}

// severityLetter maps notify severities to a single-character status letter.
// Anything unknown falls back to `I` so we never emit garbage.
func severityLetter(severity string) string {
	switch severity {
	case notify.SeverityWarn:
		return "W"
	case notify.SeverityCritical:
		return "!"
	default:
		return "I"
	}
}

// severityColor maps notify severities to the tmux color prefix. Info is the
// default style (no color override) — tmux ignores empty `#[]` so we omit it.
func severityColor(severity string) string {
	switch severity {
	case notify.SeverityWarn:
		return "#[fg=yellow]"
	case notify.SeverityCritical:
		return "#[fg=red,bold]"
	default:
		return ""
	}
}

// middotSeparator returns the ASCII separator used between the message body
// and the session name. The spec requires plain ASCII so we use `-`.
func middotSeparator() string {
	return "-"
}
