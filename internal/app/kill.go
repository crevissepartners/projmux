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

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/core/tags"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

type killCurrentSessionResolver interface {
	CurrentSessionName(ctx context.Context) (string, error)
}

type killRecentSessionsResolver interface {
	RecentSessions(ctx context.Context) ([]string, error)
}

type taggedKillExecutor interface {
	Execute(ctx context.Context, inputs lifecycle.TaggedKillInputs) (lifecycle.TaggedKillResult, error)
}

type killTagStore interface {
	List() ([]string, error)
}

type killCommand struct {
	current  killCurrentSessionResolver
	recent   killRecentSessionsResolver
	exec     taggedKillExecutor
	homeDir  func() (string, error)
	tagStore killTagStore
	storeErr error
}

func newKillCommand() *killCommand {
	client := inttmux.NewClient(inttmux.ExecRunner{})

	cmd := &killCommand{
		current: client,
		recent:  client,
		exec:    lifecycle.NewTaggedKiller(client, client),
		homeDir: os.UserHomeDir,
	}

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		cmd.storeErr = fmt.Errorf("resolve default config paths: %w", err)
		return cmd
	}

	cmd.tagStore = tags.NewDefaultStore(paths)
	return cmd
}

// Run manages kill subcommands.
func (c *killCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		printKillUsage(stderr)
		return errors.New("kill requires a subcommand")
	}

	switch fs.Arg(0) {
	case "tagged":
		return c.runTagged(fs.Args()[1:], stdout, stderr)
	case "help", "--help", "-h":
		printKillUsage(stdout)
		return nil
	default:
		printKillUsage(stderr)
		return fmt.Errorf("unknown kill subcommand: %s", fs.Arg(0))
	}
}

func (c *killCommand) runTagged(args []string, _ io.Writer, stderr io.Writer) error {
	targets, err := c.resolveTaggedTargets(args, stderr)
	if err != nil {
		return err
	}

	inputs, err := c.taggedInputs(context.Background(), targets)
	if err != nil {
		return err
	}

	if c.exec == nil {
		return fmt.Errorf("kill tagged executor is not configured")
	}

	if _, err := c.exec.Execute(context.Background(), inputs); err != nil {
		return fmt.Errorf("kill tagged sessions: %w", err)
	}

	return nil
}

func (c *killCommand) resolveTaggedTargets(args []string, stderr io.Writer) ([]string, error) {
	if len(args) != 0 {
		return normalizeTaggedItems("kill tagged", args, stderr)
	}

	store, err := c.requireTagStore()
	if err != nil {
		return nil, err
	}

	targets, err := store.List()
	if err != nil {
		return nil, fmt.Errorf("load kill tags: %w", err)
	}

	targets, err = normalizeTaggedItems("kill tagged", targets, stderr)
	if err != nil {
		return nil, fmt.Errorf("load kill tags: %w", err)
	}

	return targets, nil
}

func (c *killCommand) taggedInputs(ctx context.Context, targets []string) (lifecycle.TaggedKillInputs, error) {
	current, err := c.resolveCurrentSession(ctx)
	if err != nil {
		return lifecycle.TaggedKillInputs{}, err
	}

	recent, err := c.resolveRecentSessions(ctx)
	if err != nil {
		return lifecycle.TaggedKillInputs{}, err
	}

	homeSession, err := c.resolveHomeSession()
	if err != nil {
		return lifecycle.TaggedKillInputs{}, err
	}

	return lifecycle.TaggedKillInputs{
		CurrentSession: current,
		KillTargets:    targets,
		RecentSessions: recent,
		HomeSession:    homeSession,
	}, nil
}

func (c *killCommand) resolveCurrentSession(ctx context.Context) (string, error) {
	if c.current == nil {
		return "", fmt.Errorf("resolve current tmux session: current session resolver is not configured")
	}

	current, err := c.current.CurrentSessionName(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve current tmux session: %w", err)
	}

	return strings.TrimSpace(current), nil
}

func (c *killCommand) resolveRecentSessions(ctx context.Context) ([]string, error) {
	if c.recent == nil {
		return nil, fmt.Errorf("resolve recent tmux sessions: recent session resolver is not configured")
	}

	recent, err := c.recent.RecentSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve recent tmux sessions: %w", err)
	}

	return recent, nil
}

func (c *killCommand) resolveHomeSession() (string, error) {
	if c.homeDir == nil {
		return "", fmt.Errorf("resolve home session identity: home directory resolver is not configured")
	}

	homeDir, err := c.homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home session identity: %w", err)
	}

	cleanHome := filepath.Clean(homeDir)
	return coresessions.NewNamer(cleanHome).SessionName(cleanHome), nil
}

func (c *killCommand) requireTagStore() (killTagStore, error) {
	if c.storeErr != nil {
		return nil, fmt.Errorf("configure kill tag store: %w", c.storeErr)
	}
	if c.tagStore == nil {
		return nil, errors.New("configure kill tag store: tag store is not configured")
	}
	return c.tagStore, nil
}

func normalizeTaggedItems(command string, args []string, stderr io.Writer) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("%s requires at least 1 tagged session", command)
	}

	targets := make([]string, 0, len(args))
	seen := make(map[string]struct{}, len(args))
	for _, arg := range args {
		target := strings.TrimSpace(arg)
		if target == "" {
			printKillUsage(stderr)
			return nil, fmt.Errorf("%s requires non-empty tagged sessions", command)
		}
		if _, ok := seen[target]; ok {
			continue
		}

		seen[target] = struct{}{}
		targets = append(targets, target)
	}

	return targets, nil
}

func printKillUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux kill tagged")
	fmt.Fprintln(w, "  projmux kill tagged <session>...")
}
